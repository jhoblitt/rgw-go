# Phase 1 Unit Z: Authorization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace G's `op.OwnerOnly` stub with radosgw's `verify_permission` machinery for every phase-1 op: bucket and object ACL evaluation with canned ACLs and the S3 ACL XML in both directions, the IAM policy language (parse, principals with tenants and accounts, actions, ARNs, condition operators), bucket-policy evaluation, evaluation of the identity policies already stored on users (decision D6), object and bucket tags with their XML and the `s3:ExistingObjectTag`/`s3:ResourceTag` conditions, in radosgw's exact evaluation order and Deny precedence on both Squid v19.2.6 and Tentacle v20.2.4.

**Architecture:** Three leaf packages hold the pure logic and stored forms: `internal/policy` (G froze `Action`; Z adds the remaining action vocabulary, `ARN`, `Principal`, `Env`, `Condition`, `Statement`, `Policy`, `Parse`, `Eval`, `IsPublic`, the stored user-policy attrs and the six managed policies), `internal/acl` (phase 0 gave it the on-disk types; Z adds grant evaluation, canned ACLs, the `x-amz-grant-*` headers, the S3 XML, and the `user.rgw.public-access` block), and a new `internal/tags` (`RGWObjTags` encoding, XML, the `x-amz-tagging` header). One consumer package, `internal/authz`, implements `op.Authorizer` over them: it views `op.Identity` the way `rgw::auth::LocalApplier` does, builds the IAM condition environment from the `op.Request`, loads ACLs, bucket policy and identity policies from `BucketRecord.Attrs`, `ObjectState.Attrs` and `Identity.Attrs`, and transcribes `verify_user_permission`, `verify_bucket_permission` and `verify_object_permission` from `rgw_common.cc`. Where Squid and Tentacle disagree, a `Semantics` value derived from `denc.Release` selects the cluster's own behaviour, because both gates (s3-tests parity per release and coexistence on a shared zone) measure rgw-go against the radosgw of the same release. Nothing in Z touches RADOS; every spec runs on `memstore` and hand-built records.

**Tech Stack:** Go 1.27, `encoding/json` (streaming tokens) and `encoding/xml`, `net/netip`, `log/slog`, Ginkgo v2 and Gomega, counterfeiter fakes from G (`opfakes`), `memstore`, ceph-dencoder goldens through `hack/goldens/gen.sh` for `RGWObjTags`.

**Spec:** `docs/superpowers/specs/2026-09-25-rgw-go-design.md` §9 ("ACL, policy, quota and tenant authorization"; "ACL, policy and tagging subresources") and §8; `docs/exclusions.md` for scope; `docs/superpowers/plans/2026-09-29-phase-1/00-index.md` (unit Z in "The nine units", decision D6 in "Spec ambiguities and decisions D1-D11"); G's frozen contract in `docs/superpowers/plans/2026-09-29-phase-1/G-gateway-core.md` ("Frozen interface contract"), which this plan builds against verbatim.

Every claim about radosgw below was read in the ceph checkout at `git show v19.2.6:src/rgw/<file>` (Squid) and `v20.2.4` (Tentacle); `file:line` cites v19.2.6 unless marked `[T]` for v20.2.4.

## Global Constraints

- Module `github.com/jhoblitt/rgw-go`; `go 1.27` in `go.mod`, minor only, no `toolchain` line; every `go` command carries `-tags=ceph_preview` (the Makefile's `GO_TAGS`).
- Layout per the Go canon: everything under `internal/`; package names one lowercase word; no `util`, `common` or `helpers`.
- Dependency direction, extending G's: `policy` imports `denc` and `meta` and nothing above them (it must not import `op`, which imports `policy`); `acl` imports `denc`, `meta` and `policy` (for `PermFor`); `tags` imports `denc` only; `authz` imports `op`, `acl`, `policy`, `tags`, `meta`, `cephconf`, `denc`; `cli` wires `authz` into `op.Env.Authz`. No unit above `authz` imports `policy`'s evaluator directly: M, R, W, P and N call the `op.Verify*Permission` functions or `authz`'s explicit-resource forms.
- G's frozen contract is not edited: `op.Authorizer`'s three methods and their signatures, `op.Identity` (including `Attrs`), `policy.Action`, `policy.ParseAction`, `acl.Permission` stay as G declared them. Z extends `policy.Action` by appending constants after G's unexported `s3AllCount`, exactly as G's Task 1 Step 7 says.
- Tests are Ginkgo v2 and Gomega only, one `<pkg>_suite_test.go` per package with `RandomizeAllSpecs` and `FailOnPending`; `tags` and `authz` are new packages and get their own suites; `acl` and `policy` extend their existing ones.
- Logging is `log/slog`, JSON to stderr, static lowercase messages, snake_case attribute keys, the `…Context` variants wherever a `ctx` is in scope; policy text is never logged above debug (it may name principals); never a secret at any level.
- Errors wrap with `%w`, lowercase, unpunctuated; `errors.Is`/`errors.As` only. Packages that cannot import `op` (`acl`, `policy`, `tags`) return their own sentinels; `authz.ErrorFor` maps them to `op.Error` values with radosgw's codes and messages.
- Byte-compatibility with radosgw is the acceptance test for stored forms (`user.rgw.acl` was proven in phase 0; `user.rgw.x-amz-tagging`, `user.rgw.public-access`, `user.rgw.user-policy` and `user.rgw.managed-policy` are proven here) and behavioural equivalence for evaluation: every rule is transcribed from the cited C++ line, not designed.
- Release gating: the action vocabulary and the evaluation semantics are selected by `denc.Release` from `op.Env.Zone.Release()`; the Squid column is v19.2.6, the Tentacle column v20.2.4. An action or rule that exists only in ceph `main` is not available on either release.
- Never touch the ambient Kubernetes or Ceph cluster. The one cluster task is marked `[cluster]` and runs on the rooket clusters through T's harness.
- The librados headers on this machine are absent: cgo builds use `CGO_CFLAGS=-I/home/jhoblitt/github/ceph/src/include` and `CGO_LDFLAGS=-L$HOME/.local/lib/rgw-go`; module downloads need the sandbox disabled. Z adds no module dependency.
- Commits are Conventional Commits with the two repository trailers; each task ends in a draft PR assigned to the author, CI watched, merged on green with a merge commit under the standing authorization. The `docs/ceph-upstream-bugs.md` entries Task 12 adds are reported so the rgw-rs session can be told. Every behaviour that differs from radosgw is recorded in `docs/exclusions.md` by the task that introduces it, in the same PR: Task 7 (ACL XML escaping, D-Z7) and Task 10 (IAM group members refused, D-Z2); each is reported for the rgw-rs session too.

## Review Focus

1. **An explicit Deny in a policy stored on the user, on a shared zone.** radosgw evaluates `user.rgw.user-policy` and `user.rgw.managed-policy` on every request ([`rgw_auth.cc:135-187`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L135-L187), [`:1111-1115`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1111-L1115)), and `evaluate_iam_policies` returns Deny before any ACL or bucket policy is consulted ([`rgw_common.cc:1180-1184`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1180-L1184)). An authorizer that reads only bucket policy and ACLs lets that user through. Pinned in Task 10 (`identity policy Deny beats bucket ACL FULL_CONTROL and bucket policy Allow`).
2. **A GET of a missing key by a user who may not list the bucket.** radosgw answers 403, not 404, unless the identity holds `s3:ListBucket` with `s3:prefix` set to the key ([`rgw_op.cc:421-446`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L421-L446)); answering 404 leaks key existence. Pinned in Task 10 (`missing object: NoSuchKey only with ListBucket`).
3. **A policy that names an action the cluster's radosgw does not know.** `s3:GetObjectAttributes` parses on Tentacle and fails on Squid ([`rgw_iam_policy.cc:64-216`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L64-L216) vs [`[T]:64-231`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L64-L231)); a stored bucket policy that Squid's radosgw cannot parse makes it answer 403 to every non-system request on that bucket ([`rgw_op.cc:598-613`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L598-L613)). rgw-go must reject at PutBucketPolicy what the zone's radosgw would reject. Pinned in Task 5 (`Parse rejects a Tentacle-only action on Squid`).
4. **A bucket with `user.rgw.public-access` set by radosgw.** `IgnorePublicAcls` removes the AllUsers and AuthenticatedUsers grants from consideration ([`rgw_acl.cc:181-188`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L181-L188), [`rgw_common.cc:1419-1421`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1419-L1421)); an evaluator that ignores the attr grants anonymous reads radosgw denies. Public-access *management* is phase 2, honouring the stored block is not. Pinned in Task 6 (`Verify with ignorePublicACLs`) and Task 10.
5. **A subuser signing an S3 request.** The subuser's `perm_mask` (not the op mask) gates the ACL fallback ([`rgw_common.cc:1417-1418`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1417-L1418), [`:1588-1589`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1588-L1589), [`rgw_auth.cc:1086-1102`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1086-L1102)); a `perm_mask` of READ must not let a PUT through on an ACL that grants the parent user FULL_CONTROL. Pinned in Task 9 (`permMask of a subuser`) and Task 10.
6. **A user in an IAM group, on a shared zone.** radosgw evaluates every group's policies with the user's own ([`rgw_auth.cc:170-184`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L170-L184)), so a group's Deny refuses what the user's own policies and the ACLs allow. rgw-go cannot read groups in phase 1, so every `Verify*` method must refuse such a user before any rule runs, never evaluate without the group; only `op.Run`'s admin override may let the request through, as for any policy denial. Pinned in Task 10 (`IAM group members`) and Task 11 (end to end, with the admin override).

---

## Decisions this plan fixes

Each was open in the spec, the index's background or G's contract; each is settled here so a task implementer never has to choose.

- **D-Z1. Release-gated semantics.** Squid and Tentacle differ in: the action vocabulary (seven `s3:` and three `iam:` names added at v20.2.4, [`rgw_iam_policy.h [T]:117-123`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L117-L123), [`:152-155`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L152-L155)); `Null` ([`rgw_iam_policy.cc:857-859`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L857-L859) tests presence only; [`[T]:876-879`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L876-L879) compares `as_bool` of presence against the values); the `Not` operators `StringNotEquals`, `StringNotEqualsIgnoreCase`, `StringNotLike`, `NumericNotEquals`, `DateNotEquals`, `ArnNotEquals`/`ArnNotLike` (`:894-897`, `:903`, [`:909`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L909), [`:929-931`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L929-L931), [`:955-957`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L955-L957), [`:1000-1001`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1000-L1001) are "some pair differs"; [`[T]:911`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L911), [`:919`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L919), [`:926`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L926), [`:942`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L942), [`:965`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L965), [`:1005`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1005) are "no pair matches"); `NotIpAddress` ([`:986-1001`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L986-L1001) is already "none" on Squid; [`[T]:995`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L995) the same); `is_public` ([`:1900-1917`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L1900-L1917) calls any Allow statement without a wildcard `NotPrincipal` public; `[T]` keeps only the wildcard-`Principal` branch); `Allow` with `NotPrincipal` rejected at parse ([`[T]:771-775`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L771-L775)); `Service` principals parsed ([`[T]:591-592`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L591-L592)); `RestrictPublicBuckets` enforced (`[T] rgw_common.cc:1374-1380`, [`:1541-1547`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1541-L1547)); `x-amz-expected-bucket-owner` enforced ([`[T]:1407-1412`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1407-L1412), [`:1575-1580`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1575-L1580), [`:232-240`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L232-L240)); the admin override and the suspended-bucket and unparsable-policy bypasses keyed on `is_admin()` (`admin || system`) instead of `system_request` (`[T] rgw_process.cc:230-231`, [`rgw_op.cc [T]:424`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L424), `:455`, [`:639`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L639)). Z carries a `policy.Semantics` value chosen from `denc.Release` and applies the column the cluster runs. Rejected: Tentacle semantics everywhere (a Squid radosgw on the same zone would answer differently to the same request, failing the parity gate and the coexistence promise).
- **D-Z2. Stored user policies are evaluated; members of IAM groups are refused until phase 3.** `Identity.Attrs[user.rgw.user-policy]` (a `map<string,string>`, [`rgw_auth.cc:85-95`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L85-L95)) and `Identity.Attrs[user.rgw.managed-policy]` (a `set<string>` of ARNs resolved against the six built-in texts, [`:97-108`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L97-L108), [`rgw_iam_managed_policy.cc:26-176`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_managed_policy.cc#L26-L176)) are decoded on every request, read-only. Group policies are not: radosgw loads, at authentication, the inline and managed policies of every group in `RGWUserInfo::group_ids` (`load_account_and_policies`, [`rgw_auth.cc:170-184`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L170-L184), over `load_group_policies`, [`:110-133`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L110-L133), the same lines `[T]`; the field is [`rgw_common.h:603`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L603), [`[T]:627`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L627), and phase 0's `meta.UserInfo.GroupIDs`) and evaluates them with the user's own, but reading a group needs a group store no phase-1 interface offers, and groups are created only through the IAM API rgw-go serves from phase 3, though they may already exist on a zone radosgw serves. Evaluating a member without them would allow what a group's Deny makes radosgw refuse, so the `Evaluator` fails closed (owner decision 6): a request whose identity has a non-empty `User.GroupIDs` is `op.ErrAccessDenied` from every exported `Verify*` method before any rule runs, and the user is logged once per process at warn level (Task 10's `deniedAsGroupMember`). `op.Run`'s admin override still applies, as it does to every policy denial ([`rgw_process.cc:228-236`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L228-L236)), so an admin or system identity in a group proceeds. This is a difference from radosgw, which Task 10 records in `docs/exclusions.md`'s coexistence section.
- **D-Z3. `perm` is the caller's.** G's `Authorizer.VerifyBucket(…, a policy.Action, perm acl.Permission)` carries the ACL permission the fallback tests; radosgw derives it as `op_to_perm(op)` ([`rgw_iam_policy.h:237-318`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L237-L318)). Z exports `acl.PermFor(a)` (the transcription) and uses the `perm` it is handed; M, R, W and P pass `acl.PermFor(action)`. Reported to G as a redundancy, not changed.
- **D-Z4. The IAM environment is derived from the request inside `authz`.** `rgw_build_iam_environment` runs before `verify_permission` ([`rgw_rest.cc:1883-1890`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1883-L1890)) and each op adds its own keys (`s3:prefix`, `s3:x-amz-acl`, …). G's `op.Request` has no slot for it and its `Env` field is the process environment. Z computes the whole environment from `r` per action (Task 9's table), so no op has to remember a key.
- **D-Z5. The missing-object rule lives in `VerifyObject`.** `read_obj_policy`'s "404 only if you may list" branch ([`rgw_op.cc:421-446`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L421-L446)) runs in `read_permissions` in radosgw; here `VerifyObject` applies it when `r.ObjState == nil || !r.ObjState.Exists`, returning `op.ErrNoSuchKey` or `op.ErrAccessDenied`. R, W and P call `VerifyObject` before treating a missing object as 404 (contract appendix).
- **D-Z6. Explicit-resource forms.** CopyObject checks the source with the source bucket's ACL and policy but the request's identity, environment, public-access block and requester-pays ([`rgw_op.cc:5407-5460`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5407-L5460), [`:3920-3963`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3920-L3963); `verify_bucket_permission(this, s, ARN(src), s->user_acl, src_bucket_acl, src_policy, …)` with `s` still the destination request, identical at v20.2.4 [`:6020-6023`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6020-L6023)), and DeleteObjects checks every key with the object's ARN against the bucket (`:6826-6838`). `authz.Evaluator` implements `op.Authorizer`'s `VerifyBucketIn` and `VerifyObjectIn` (G's interface carries them beside the three original methods; `OwnerOnly` implements them too), so W and P reach them through `op.VerifyBucketPermissionIn`/`op.VerifyObjectPermissionIn` with no type assertion and no request copy.
- **D-Z7. Two radosgw defects are mirrored, one is not.** Mirrored: `aws:CurrentTime` is the epoch as decimal seconds and `aws:EpochTime` the ISO-8601 string, the reverse of AWS ([`rgw_op.cc:852-853`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L852-L853)), because a `DateLessThan` works on either and a `StringEquals` on the value must match radosgw; `is_public`'s over-approximation on Squid (D-Z1). Not mirrored: `to_xml` writes owner and grantee display names and ids unescaped ([`rgw_acl_s3.cc:172-180`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl_s3.cc#L172-L180), [`:246-280`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl_s3.cc#L246-L280), the same lines `[T]`), producing invalid XML for a display name containing `&` or `<`; rgw-go escapes them with `xmltext.Escape` (G Task 4), the escaping radosgw's `XMLFormatter` gives the text of every other S3 document (`xml_stream_escaper`, [src/common/escape.cc:134-169](https://github.com/ceph/ceph/blob/v19.2.6/src/common/escape.cc#L134-L169)) (owner decision 12c). All three go into `docs/ceph-upstream-bugs.md` (Task 12); the escaping is also a difference from radosgw, which Task 7, the task that renders the document, records in `docs/exclusions.md`'s coexistence section.
- **D-Z8. Quota.** Unit Z's scope in the index puts "the op-level check call" in Z; G's contract already has it as `op.StatsStore.CheckQuota` (M implements, W calls). Z adds nothing for quota.

---

## File structure

```
internal/policy/action.go          modify: the shared names table grows to ActionCount (Task 1)
internal/policy/actions.go         Z's continuation of the enum: s3-object-lambda, iam, sts, sns, organizations; Known
internal/policy/actionset.go       ActionSet bitset over ActionCount; the per-service All values
internal/policy/match.go           MatchWildcards (fnmatch), MatchPolicy (colon segments), MatchAction
internal/policy/arn.go             Partition, Service, ARN, ParseARN, Match, String, BucketARN, ObjectARN
internal/policy/principal.go       Principal
internal/policy/env.go             Env, the condition environment multimap
internal/policy/semantics.go       Semantics, SemanticsFor(denc.Release)
internal/policy/condition.go       Operator, Condition, Eval, the typed conversions, MaskedIP
internal/policy/statement.go       Statement, Eval, EvalPrincipal, EvalConditions
internal/policy/policy.go          Policy, Eval, IsPublic
internal/policy/parse.go           Parse, ParseOptions, ParseError, stripComments, the token state machine
internal/policy/stored.go          DecodeUserPolicies, DecodeManagedPolicies, ManagedPolicy
internal/policy/managed.go         the six managed policy texts
internal/policy/testdata/           radosgw's example policies as .json files
internal/acl/perm.go               PermFor, PermReadObjs, PermWriteObjs, PermInvalid
internal/acl/eval.go               Identity, Perm, Verify, IsPublic, DefaultPolicy, AddGrant, RemoveCanonUserGrant
internal/acl/s3xml.go              Resolver, ParseS3XML, MarshalS3XML, Canned, FromHeaders, group URIs
internal/acl/publicaccess.go       PublicAccessBlock, Encode, DecodePublicAccessBlock
internal/tags/{doc,tags,xml}.go    Set, Tag, limits, Encode/Decode, ParseXML, MarshalXML, ParseHeader
internal/tags/testdata/goldens/    RGWObjTags goldens from gen.sh
internal/authz/doc.go
internal/authz/config.go           Config, ConfigFrom
internal/authz/identity.go         the rgw::auth::Identity view over op.Identity
internal/authz/env.go              BuildEnv and the per-action key table
internal/authz/load.go             bucketACL, objectACL, bucketPolicy, identityPolicies, publicAccess, tags into env
internal/authz/evaluator.go        Evaluator: VerifyUser, VerifyBucket, VerifyObject, VerifyBucketIn, VerifyObjectIn
internal/authz/write.go            BuildACL, BuildDefaultACL, ParseBucketPolicy, UserResolver
internal/authz/errors.go           ErrorFor
internal/cli/serve.go              modify: env.Authz = authz.New(cfg)
hack/goldens/types.txt             modify: RGWObjTags internal/tags
docs/ceph-upstream-bugs.md         modify: three entries (Task 12)
docs/exclusions.md                 modify: ACL XML escaping (Task 7); IAM group members refused (Task 10)
```

## Task index

| # | Task | Depends on | Cluster |
|---|---|---|---|
| 1 | `policy`: action vocabulary, `Known`, `ActionSet`, wildcard and action matching | G Task 1 merged | no |
| 2 | `policy`: `ARN`, `Principal`, `Env`, `Semantics` | 1 | no |
| 3 | `policy`: `Condition` operators and typed conversions | 2 | no |
| 4 | `policy`: `Statement`, `Policy`, `Eval`, `IsPublic` with radosgw's cases | 3 | no |
| 5 | `policy`: `Parse`, stored user policies, managed policies | 4 | no |
| 6 | `acl`: evaluation, `PermFor`, defaults, `PublicAccessBlock` | 1 | no |
| 7 | `acl`: S3 XML, canned ACLs, grant headers, `Resolver` | 6 | no |
| 8 | `tags`: `RGWObjTags`, XML, header | none | no |
| 9 | `authz`: identity view, environment builder, loaders | 5, 6, 8 | no |
| 10 | `authz`: the `Evaluator` | 9 | no |
| 11 | `authz`: write-side helpers, `ErrorFor`, `cli serve` wiring, end-to-end | 7, 10 | no |
| 12 | Gate: s3-tests ACL, policy and tagging groups; upstream-bug entries | 11, M, R, W, T-b | `[cluster]` |

Tasks 1 to 5 are one chain; 6 and 7 another; 8 stands alone; the three chains run in parallel after G's interface freeze merges. Tasks 9 to 11 join them. Task 12 waits for the read and write paths.

---

### Task 1: `policy`: action vocabulary, `Known`, `ActionSet`, wildcard and action matching

**Files:**
- Modify: `internal/policy/action.go` (the names table becomes `[ActionCount]string`; `String` and `ParseAction` unchanged in behaviour)
- Create: `internal/policy/actions.go`, `internal/policy/actionset.go`, `internal/policy/match.go`, `internal/policy/actions_test.go`, `internal/policy/match_test.go`

**Interfaces:**
- Consumes: G's `policy.Action`, `S3All`, `s3AllCount`, `String`, `ParseAction`; `denc.Release`.
- Produces:

```go
package policy

// The actions after the s3 block, in rgw::IAM::action_t order
// (src/rgw/rgw_iam_policy.h:117-206 at v19.2.6, :124-216 at v20.2.4).
const (
	S3ObjectLambdaGetObject Action = s3AllCount + iota
	S3ObjectLambdaListBucket
	S3ObjectLambdaAll
	IAMPutUserPolicy
	IAMGetUserPolicy
	IAMDeleteUserPolicy
	IAMListUserPolicies
	IAMAttachUserPolicy
	IAMDetachUserPolicy
	IAMListAttachedUserPolicies
	IAMCreateRole
	IAMDeleteRole
	IAMModifyRoleTrustPolicy
	IAMGetRole
	IAMListRoles
	IAMPutRolePolicy
	IAMGetRolePolicy
	IAMListRolePolicies
	IAMDeleteRolePolicy
	IAMAttachRolePolicy
	IAMDetachRolePolicy
	IAMListAttachedRolePolicies
	IAMCreateOIDCProvider
	IAMDeleteOIDCProvider
	IAMGetOIDCProvider
	IAMListOIDCProviders
	IAMAddClientIDToOIDCProvider      // Tentacle
	IAMRemoveClientIDFromOIDCProvider // Tentacle; spelled "iam:RemoveCientIdFromOIDCProvider" there
	IAMUpdateOIDCProviderThumbprint   // Tentacle
	IAMTagRole
	IAMListRoleTags
	IAMUntagRole
	IAMUpdateRole
	IAMCreateUser
	IAMGetUser
	IAMUpdateUser
	IAMDeleteUser
	IAMListUsers
	IAMCreateAccessKey
	IAMUpdateAccessKey
	IAMDeleteAccessKey
	IAMListAccessKeys
	IAMCreateGroup
	IAMGetGroup
	IAMUpdateGroup
	IAMDeleteGroup
	IAMListGroups
	IAMAddUserToGroup
	IAMRemoveUserFromGroup
	IAMListGroupsForUser
	IAMPutGroupPolicy
	IAMGetGroupPolicy
	IAMListGroupPolicies
	IAMDeleteGroupPolicy
	IAMAttachGroupPolicy
	IAMDetachGroupPolicy
	IAMListAttachedGroupPolicies
	IAMGenerateCredentialReport
	IAMGenerateServiceLastAccessedDetails
	IAMSimulateCustomPolicy
	IAMSimulatePrincipalPolicy
	IAMAll
	STSAssumeRole
	STSAssumeRoleWithWebIdentity
	STSGetSessionToken
	STSTagSession
	STSAll
	SNSGetTopicAttributes
	SNSDeleteTopic
	SNSPublish
	SNSSetTopicAttributes
	SNSCreateTopic
	SNSListTopics
	SNSAll
	OrganizationsDescribeAccount
	OrganizationsDescribeOrganization
	OrganizationsDescribeOrganizationalUnit
	OrganizationsDescribePolicy
	OrganizationsListChildren
	OrganizationsListParents
	OrganizationsListPoliciesForTarget
	OrganizationsListRoots
	OrganizationsListPolicies
	OrganizationsListTargetsForPolicy
	OrganizationsAll
	// ActionCount is rgw::IAM::allCount.
	ActionCount
)

// Known reports whether the radosgw of release r accepts a in a policy: the
// actpairs table of src/rgw/rgw_iam_policy.cc at that release.
func Known(a Action, r denc.Release) bool

// ActionSet is rgw::IAM::Action_t, one bit per Action.
type ActionSet [(ActionCount + 63) / 64]uint64
func (s *ActionSet) Set(a Action)
func (s ActionSet) Has(a Action) bool
func (s ActionSet) IsZero() bool
func (s ActionSet) Equal(o ActionSet) bool
func (s ActionSet) Contains(o ActionSet) bool // every bit of o is in s
func (s *ActionSet) Union(o ActionSet)
// The per-service values of rgw_iam_policy.h:225-232.
func S3AllValue() ActionSet
func S3ObjectLambdaAllValue() ActionSet
func IAMAllValue() ActionSet
func STSAllValue() ActionSet
func SNSAllValue() ActionSet
func OrganizationsAllValue() ActionSet
func AllValue() ActionSet

// MatchWildcards is match_wildcards (rgw_string.cc:7-22): fnmatch(3) with no
// FNM_PATHNAME and no FNM_PERIOD, so * matches any run including '/', ? any
// one byte, [...] a class with ranges and a leading ! or ^ negation, \ an
// escape; caseInsensitive is FNM_CASEFOLD.
func MatchWildcards(pattern, input string, caseInsensitive bool) bool

// MatchPolicy is match_policy (rgw_common.cc:2167-2189): the pattern and the
// input are split on ':' and matched segment by segment, every segment with
// MatchWildcards; the segments must be equal in number. action selects
// MATCH_POLICY_ACTION, which makes the match case-insensitive.
func MatchPolicy(pattern, input string, action bool) bool

// MatchAction sets in dst the bit of every action of release r whose name
// matches pattern (do_string, rgw_iam_policy.cc:620-628), then the service
// All bit for each service whose actions are all set (:629-664). It returns
// false when no action matched, which the parser turns into
// "`<pattern>` is not a valid action".
func MatchAction(dst *ActionSet, pattern string, r denc.Release) bool
```

The names for Z's rows are explicit strings in the shared table, not derived from the constant names, because radosgw's casing differs from Go's (`iam:AddClientIdToOIDCProvider`, `iam:RemoveCientIdFromOIDCProvider` with its v20.2.4 typo, `s3-object-lambda:GetObject`). The `All` values render as `<service>:*` (`iam:*`, `sts:*`, `sns:*`, `organizations:*`, `s3-object-lambda:*`) and `ParseAction` accepts those spellings, as G's `S3All` does for `s3:*`.

`Known`: every action is known on Squid except `S3PostBucketLogging`, `S3GetObjectAttributes`, `S3GetObjectVersionAttributes`, `S3ReplicateDelete`, `S3ReplicateObject`, `S3GetObjectVersionForReplication`, `S3ReplicateTags`, `IAMAddClientIDToOIDCProvider`, `IAMRemoveClientIDFromOIDCProvider`, `IAMUpdateOIDCProviderThumbprint` (Tentacle, [`rgw_iam_policy.cc [T]:64-231`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L64-L231)), and `S3PutAccountPublicAccessBlock`, `S3GetAccountPublicAccessBlock`, which G transcribed from ceph `main` and which neither v19.2.6 nor v20.2.4 knows: `Known` is false for them on both releases. The `All` constants are known everywhere (they are never parsed from a name row; `*` and `<service>:*` set them through `MatchAction`).

- [ ] **Step 1: Write the failing specs**

`internal/policy/actions_test.go`, package `policy_test`:

```go
var _ = Describe("actions", func() {
	It("continues the enum where G's s3 block ends", func() {
		Expect(policy.S3ObjectLambdaGetObject).To(Equal(policy.S3All + 2), "s3AllCount is S3All+1")
		Expect(policy.IAMAll.String()).To(Equal("iam:*"))
		Expect(policy.SNSPublish.String()).To(Equal("sns:Publish"))
		Expect(policy.S3ObjectLambdaListBucket.String()).To(Equal("s3-object-lambda:ListBucket"))
		Expect(policy.IAMRemoveClientIDFromOIDCProvider.String()).To(Equal("iam:RemoveCientIdFromOIDCProvider"))
	})
	It("round-trips every action through ParseAction", func() {
		for a := policy.Action(0); a < policy.ActionCount; a++ {
			got, ok := policy.ParseAction(a.String())
			Expect(ok).To(BeTrue(), a.String())
			Expect(got).To(Equal(a), a.String())
		}
	})
	DescribeTable("Known follows the release's actpairs table",
		func(a policy.Action, squid, tentacle bool) {
			Expect(policy.Known(a, denc.Squid)).To(Equal(squid), "squid")
			Expect(policy.Known(a, denc.Tentacle)).To(Equal(tentacle), "tentacle")
		},
		Entry("s3:GetObject", policy.S3GetObject, true, true),
		Entry("s3:GetObjectAttributes", policy.S3GetObjectAttributes, false, true),
		Entry("s3:PostBucketLogging", policy.S3PostBucketLogging, false, true),
		Entry("iam:UpdateOIDCProviderThumbprint", policy.IAMUpdateOIDCProviderThumbprint, false, true),
		Entry("s3:PutAccountPublicAccessBlock (main only)", policy.S3PutAccountPublicAccessBlock, false, false),
		Entry("iam:*", policy.IAMAll, true, true),
	)
	It("ActionSet sets, tests and unions", func() {
		var s policy.ActionSet
		Expect(s.IsZero()).To(BeTrue())
		s.Set(policy.S3GetObject)
		s.Set(policy.OrganizationsAll)
		Expect(s.Has(policy.S3GetObject)).To(BeTrue())
		Expect(s.Has(policy.S3PutObject)).To(BeFalse())
		Expect(s.Has(policy.OrganizationsAll)).To(BeTrue())
		Expect(policy.AllValue().Contains(s)).To(BeTrue())
		Expect(policy.S3AllValue().Has(policy.S3All)).To(BeFalse(), "set_cont_bits(0, s3All) excludes s3All itself")
		Expect(policy.IAMAllValue().Has(policy.IAMPutUserPolicy)).To(BeTrue())
		Expect(policy.IAMAllValue().Has(policy.S3ObjectLambdaAll)).To(BeFalse())
	})
})
```

`internal/policy/match_test.go` transcribes `TEST(MatchWildcards, Simple|QuestionMark|Asterisk)` and `TEST(MatchPolicy, Action|ARN)` from [`src/test/rgw/test_rgw_iam_policy.cc:1369-1475`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L1369-L1475); the Simple and QuestionMark cases verbatim (`("", "")` true, `("", "abc")` false, `("abc", "abC")` false and true case-insensitively, `("?bc", "abc")` true, `("???", "abcd")` false, `("*", "")` true, `("", "*")` false, `("a*c*e", "abBce")` true), plus classes and escapes fnmatch defines: `("[ab]c", "bc")` true, `("[!a]c", "bc")` true, `("[a-c]", "b")` true, `("\\*", "*")` true, `("\\*", "a")` false, `("a*", "a/b/c")` true (no FNM_PATHNAME). `MatchPolicy`: `("s3:Get*", "s3:GetObject", true)` true, `("S3:getobject", "s3:GetObject", true)` true (action matching is case-insensitive), `("s3:*", "s3-object-lambda:GetObject", true)` false (segments differ), `("arn:aws:s3:::b/*", "arn:aws:s3:::b/k", false)` true, `("arn:aws:s3:::B/*", "arn:aws:s3:::b/k", false)` false, `("a:b", "a:b:c", false)` false. `MatchAction`: `"s3:Get*"` on Squid sets `S3GetObject` and not `S3GetObjectAttributes`; on Tentacle sets both; `"s3:*"` sets `S3All`; `"iam:*"` sets `IAMAll` and every iam bit; `"*"` is not handled here (the parser sets `AllValue` first, [`rgw_iam_policy.cc:615-618`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L615-L618)); `"s3:Nope"` returns false.

- [ ] **Step 2: Run to see them fail**

Run: `go test -tags=ceph_preview ./internal/policy/...`
Expected: FAIL, undefined `policy.S3ObjectLambdaGetObject`, `policy.Known`, `policy.ActionSet`, `policy.MatchWildcards`.

- [ ] **Step 3: Implement**

`actions.go` holds the constant block above and the `names` tail; `action.go`'s table becomes `var names = [ActionCount]string{...}` with G's derived entries kept and Z's appended literally. `Known` is a `switch a` listing the Tentacle-only and the unreleased constants. `actionset.go` is a plain `[4]uint64` bitset with `Set(a)` doing `s[a/64] |= 1 << (a % 64)`; the `*AllValue` functions build `set_cont_bits(start, end)` by setting `[start, end)`.

`match.go`: `MatchWildcards` is a hand-written fnmatch: iterate the pattern; `*` tries every suffix (backtracking on the last star as glibc does); `?` consumes one byte; `[` scans to the closing `]` honouring `!`/`^`, `a-z` ranges and `\` escapes inside; `\` escapes the next byte; case folding lowercases both bytes when asked. Bytes, not runes: fnmatch(3) in radosgw compares `char`s. `MatchPolicy` splits both strings on `:` with `strings.Split` and walks them together exactly as `:2171-2188` does (unequal counts fail; each segment through `MatchWildcards`). `MatchAction` ranges over `Action(0) ..< ActionCount`, skipping the `All` constants and actions not `Known` on `r`, testing `MatchPolicy(pattern, a.String(), true)`; afterwards, for each service, if `dst.Contains(<Service>AllValue())` set the service's `All` bit ([`:629-664`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L629-L664)).

- [ ] **Step 4: Run to see them pass**

Run: `go test -tags=ceph_preview ./internal/policy/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/policy
git commit -m "feat(policy): complete the IAM action vocabulary with release gating and radosgw's wildcard matching"
```

---

### Task 2: `policy`: `ARN`, `Principal`, `Env`, `Semantics`

**Files:**
- Create: `internal/policy/arn.go`, `internal/policy/principal.go`, `internal/policy/env.go`, `internal/policy/semantics.go`, `internal/policy/arn_test.go`, `internal/policy/principal_test.go`

**Interfaces:**
- Consumes: Task 1's `MatchWildcards`; `meta.ObjKey`; `denc.Release`.
- Produces:

```go
package policy

// Partition and Service are rgw::Partition and rgw::Service (rgw_arn.h:12-33).
type Partition uint8
const (
	PartitionAWS Partition = iota
	PartitionAWSCN
	PartitionAWSUSGov
	PartitionWildcard
)
type Service uint8 // the 79 services of rgw_arn.cc:32-116 in that order, then ServiceWildcard

// ARN is rgw::ARN: arn:<partition>:<service>:<region>:<account>:<resource>.
type ARN struct {
	Partition Partition
	Service   Service
	Region    string
	Account   string
	Resource  string
}

// BucketARN is ARN(const rgw_bucket&) (rgw_arn.cc:137-142): aws, s3, no
// region, the tenant, the bucket name.
func BucketARN(tenant, bucket string) ARN
// ObjectARN is ARN(const rgw_obj&) (:126-135): the resource is "<bucket>/<key name>".
func ObjectARN(tenant, bucket, key string) ARN
// IAMARN is ARN(resource_name, type, tenant, has_path) (:154-163): aws, iam, the
// tenant, "<type>/<name>" (or "<type><name>" when hasPath).
func IAMARN(name, typ, tenant string, hasPath bool) ARN

// ParseARN is ARN::parse (:165-187). With wildcards, "*" alone is the
// all-wildcard ARN and every field may be "*" (partition and service only
// exactly "*"); the resource field then may not contain ':'. Without
// wildcards the first four fields may not contain '*' and the resource may
// contain anything.
func ParseARN(s string, wildcards bool) (ARN, bool)
func (a ARN) String() string // ARN::to_string (:189-300)
// Match is ARN::match (:319-344): a is the pattern. A wildcard partition or
// service in the candidate never matches; region and account match
// case-insensitively, the resource case-sensitively, all through MatchWildcards.
func (a ARN) Match(candidate ARN) bool

// Principal is rgw::auth::Principal (rgw_basic_types.h:143-240).
type PrincipalKind uint8
const (
	PrincipalUser PrincipalKind = iota
	PrincipalRole
	PrincipalAccount
	PrincipalWildcard
	PrincipalOIDCProvider
	PrincipalAssumedRole
	PrincipalService // Tentacle only (rgw_basic_types.h [T]:187, :217)
)
type Principal struct {
	Kind    PrincipalKind
	Account string // the tenant or account id; Principal::get_account
	ID      string // user name, role name, role session, or the service name
	IDPURL  string
}
func WildcardPrincipal() Principal
func UserPrincipal(account, id string) Principal
func RolePrincipal(account, id string) Principal
func AccountPrincipal(account string) Principal
func OIDCProviderPrincipal(url string) Principal
func AssumedRolePrincipal(account, id string) Principal
func ServicePrincipal(name string) Principal
func (p Principal) IsWildcard() bool // and IsUser, IsRole, IsAccount, IsOIDCProvider, IsAssumedRole, IsService

// Env is rgw::IAM::Environment, a multimap of condition keys to values.
type Env map[string][]string
func (e Env) Add(key, value string)          // emplace; a value may be empty
func (e Env) Lookup(key string) []string      // nil when absent
func (e Env) Remove(key, value string)        // erase every pair equal to (key, value)
func (e Env) Clone() Env

// Semantics selects the release's evaluation rules, so a policy evaluates as
// the radosgw of the cluster's release evaluates it.
type Semantics struct {
	// NullTestsValues: Null compares as_bool(presence) with the values (Tentacle)
	// instead of ignoring them (Squid).
	NullTestsValues bool
	// NotMeansNone: the Not operators mean "no pair matches" (Tentacle)
	// instead of "some pair differs" (Squid).
	NotMeansNone bool
	// PublicNeedsWildcardPrincipal: is_public requires an Allow statement
	// with a wildcard Principal (Tentacle); Squid also counts any Allow
	// statement whose NotPrincipal has no wildcard.
	PublicNeedsWildcardPrincipal bool
	// RejectAllowWithNotPrincipal fails parsing an Allow statement that
	// carries NotPrincipal (Tentacle).
	RejectAllowWithNotPrincipal bool
	// ServicePrincipals parses "Service" principals (Tentacle); Squid ignores
	// or rejects them like any unsupported principal type.
	ServicePrincipals bool
	// RestrictPublicBuckets and ExpectedBucketOwner are enforced in the
	// authorizer (Tentacle).
	RestrictPublicBuckets bool
	ExpectedBucketOwner   bool
	// AdminBypass: the suspended-bucket and unparsable-policy bypasses key on
	// admin||system (Tentacle) instead of the system flag alone (Squid).
	AdminBypass bool
}
func SemanticsFor(r denc.Release) Semantics // all false for Squid, all true for Tentacle
```

- [ ] **Step 1: Write the failing specs**

`arn_test.go`: `ParseARN("arn:aws:s3:::example_bucket", true)` gives `{PartitionAWS, ServiceS3, "", "", "example_bucket"}`; `ParseARN("*", true)` gives `{PartitionWildcard, ServiceWildcard, "*", "*", "*"}`; `ParseARN("*", false)` is not ok; `ParseARN("arn:aws:s3:::b/k:x", true)` is not ok while `ParseARN("arn:aws:s3:::b/k:x", false)` is ok with resource `b/k:x` (the two regexes of [`:166-173`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_basic_types.h#L166-L173)); `ParseARN("arn:aws:iam::tenant:user/A", false)` ok; `ParseARN("arn:aws:nope:::x", true)` not ok; `ParseARN("arn:aws-cn:s3:::x", true)` partition `PartitionAWSCN`; `String()` round-trips each and renders the wildcard partition and service as `*`. `Match`: pattern `arn:aws:s3:::b/*` matches `ObjectARN("", "b", "k/l")`; pattern with account `*` matches any tenant; pattern account `T` does not match tenant `t`? It does: account matching is case-insensitive (`:335-337`), so `Expect(match).To(BeTrue())`; the resource is case-sensitive: `b/*` does not match `B/k`; a candidate with a wildcard service never matches (`:320-329`). `BucketARN("t", "b").String() == "arn:aws:s3::t:b"`; `ObjectARN("", "b", "k").String() == "arn:aws:s3:::b/k"`; `IAMARN("r", "role", "t", false).String() == "arn:aws:iam::t:role/r"`.

`principal_test.go`: the constructors set `Kind`, `Account`, `ID`; `Env.Add` keeps duplicates and order; `Remove("k", "v")` removes only equal pairs; `SemanticsFor(denc.Squid)` is the zero value and `SemanticsFor(denc.Tentacle)` has every field true.

- [ ] **Step 2: Run to see them fail**

Run: `go test -tags=ceph_preview ./internal/policy/...`
Expected: FAIL, undefined identifiers.

- [ ] **Step 3: Implement**

`arn.go`: two regular expressions mirroring `rx_wild` and `rx_no_wild` ([`:166-173`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_arn.cc#L166-L173)), the partition table ([`:13-28`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_arn.cc#L13-L28)) and the service table ([`:32-116`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_arn.cc#L32-L116), transcribed as a `map[string]Service` plus the reverse slice for `String`). `Match` as [`:319-344`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_arn.cc#L319-L344). `principal.go` is data. `env.go` is a thin map. `semantics.go` is `return Semantics{...all: r >= denc.Tentacle}`.

- [ ] **Step 4: Run to see them pass**, then **Step 5: Commit**

```sh
git commit -am "feat(policy): add ARN, Principal, the condition environment and release semantics"
```

---

### Task 3: `policy`: `Condition` operators and typed conversions

**Files:**
- Create: `internal/policy/condition.go`, `internal/policy/condition_test.go`

**Interfaces:**
- Consumes: Task 2's `Env`, `Semantics`, `MatchWildcards`, `MatchPolicy`.
- Produces:

```go
package policy

// Operator is the condition-operator subset of rgw::IAM::TokenID
// (rgw_iam_policy_keywords.h:31-55), named as the gperf table spells them.
type Operator uint8
const (
	OpStringEquals Operator = iota
	OpStringNotEquals
	OpStringEqualsIgnoreCase
	OpStringNotEqualsIgnoreCase
	OpStringLike
	OpStringNotLike
	OpForAllValuesStringEquals
	OpForAnyValueStringEquals
	OpForAllValuesStringLike
	OpForAnyValueStringLike
	OpForAllValuesStringEqualsIgnoreCase
	OpForAnyValueStringEqualsIgnoreCase
	OpNumericEquals
	OpNumericNotEquals
	OpNumericLessThan
	OpNumericLessThanEquals
	OpNumericGreaterThan
	OpNumericGreaterThanEquals
	OpDateEquals
	OpDateNotEquals
	OpDateLessThan
	OpDateLessThanEquals
	OpDateGreaterThan
	OpDateGreaterThanEquals
	OpBool
	OpBinaryEquals
	OpIpAddress
	OpNotIpAddress
	OpArnEquals
	OpArnNotEquals
	OpArnLike
	OpArnNotLike
	OpNull
)
func (o Operator) String() string
// ParseOperator accepts the exact gperf spellings ("ForAllValues:StringEquals").
func ParseOperator(s string) (Operator, bool)

// Condition is rgw::IAM::Condition (rgw_iam_policy.h:348-363).
type Condition struct {
	Op        Operator
	Key       string
	IfExists  bool
	IsRuntime bool // the last value is "${key}" (rgw_iam_policy.cc:701-717)
	Values    []string
}

// Eval is Condition::eval (rgw_iam_policy.cc:854-1006) under sem.
func (c Condition) Eval(env Env, sem Semantics) bool

// MaskedIP is rgw::IAM::MaskedIP (rgw_iam_policy.h:329-345): 128 bits and a
// prefix; a v4 address sits in the low 32 bits.
type MaskedIP struct {
	V6     bool
	Addr   [16]byte
	Prefix uint
}
// ParseMaskedIP is Condition::as_network (:1008-1068).
func ParseMaskedIP(s string) (MaskedIP, bool)
func (m MaskedIP) String() string // operator<< (:811-840): dotted quad or 8 bare hextets, then "/prefix"
func (m MaskedIP) Equal(o MaskedIP) bool // operator== (rgw_iam_policy.h:341-345): compare after shifting both by the larger host-part width

// The typed conversions of rgw_iam_policy.h:365-433, exported for authz's specs.
func AsNumber(s string) (float64, bool)
func AsDate(s string) (time.Time, bool)
func AsBool(s string) bool
func AsBinary(s string) ([]byte, bool)
```

Evaluation, transcribed from `:854-1006` with the release column:

1. `vals := env.Lookup(c.Key)`. `OpNull`: Squid returns `vals == nil`; Tentacle (`sem.NullTestsValues`) computes `present := "true"` when absent else `"false"` and returns `typedAny(equal, AsBool, present, c.Values)` ([`[T]:876-879`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L876-L879)).
2. Absent key: `ForAllValues:*` operators return true; every other operator returns `c.IfExists` (`:862-869`).
3. `IsRuntime`: `k := strings.TrimSuffix(strings.TrimPrefix(c.Values[len-1], "${"), "}")`; the effective values are `env.Lookup(k)` (all of them) instead of `c.Values` ([`:871-878`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L871-L878)). Only the string and ARN operators consult the effective values; the typed operators always use `c.Values` (as [`:920-1006`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L920-L1006) do).
4. `s := vals[0]` is the single value the typed operators convert (`i->second`).
5. String operators over every pair `(v in vals, d in effective)`: `StringEquals`/`ForAnyValue:StringEquals` any equal; `StringEqualsIgnoreCase` any `strings.EqualFold`; `StringLike` any `MatchWildcards(d, v, false)`; `ForAllValues:*` every `v` has some matching `d` (`andible`, [`rgw_iam_policy.h:390-405`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L390-L405)); the three `Not` string operators: Squid `orrible(not_fn(f))`, some pair for which `f` is false; Tentacle `multimap_none`, no pair for which `f` is true.
6. Typed operators (`shortible`/`typed_any`): convert `s`; unconvertible → false; for each `d` convertible, `f(x(s), x(d))` true → true; else false. `NumericNotEquals` and `DateNotEquals`: Squid `shortible(not_fn(equal))` (some `d` differs), Tentacle `typed_none(equal)` (none equal). `Bool`: `AsBool` on both sides, equal. `BinaryEquals`: `AsBinary` both, `bytes.Equal`. `IpAddress`: `ParseMaskedIP` both, `Equal`. `NotIpAddress` is the same on both releases: `s` unparsable → false; any `d` equal → false; else true (`:986-1001`).
7. `ArnEquals` and `ArnLike` are identical ([`:1003-1006`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1003-L1006)): over every pair, `arnLike(v, d)`: `v` must contain exactly five `:` ([`:845-852`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L845-L852)) and `MatchPolicy(d, v, false)`; the `Not` forms follow rule 5's release split.

Conversions: `AsNumber` is `strtod` over the whole string (`strconv.ParseFloat` after trimming leading whitespace as strtod does; a trailing remainder fails). `AsDate` ([`rgw_iam_policy.h:380-397`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L380-L397)): a whole-string number is epoch seconds with a fraction; otherwise ceph's `from_iso_8601` with `ws_terminates=false`, which accepts `YYYY`, `YYYY-MM`, `YYYY-MM-DD`, `YYYY-MM-DDTHH:MMZ`, `YYYY-MM-DDTHH:MM:SSZ` and `YYYY-MM-DDTHH:MM:SS.fffffffffZ` (`src/common/iso_8601.cc`, the `YMDhmsn` family) with a literal `Z`. `AsBool` ([`:399-416`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L399-L416)): empty or `false` case-insensitively → false; a whole-string number → non-zero and not NaN; anything else → true. `AsBinary` ([`:418-433`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L418-L433)): standard base64; padding optional (`bufferlist::decode_base64` tolerates its absence).

- [ ] **Step 1: Write the failing specs**

`condition_test.go` transcribes `IPPolicyTest` ([`test_rgw_iam_policy.cc:1031-1095`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L1031-L1095), [`:1143-1200`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L1143-L1200) for the parsed values): `ParseMaskedIP("192.168.1.0/24")` equals `{false, 192.168.1.0, 24}` and prints `192.168.1.0/24`; `"192.168.1.1"` gets prefix 32; `"2001:db8:85a3:0:0:8a2e:370:7330/124"` prints `2001:db8:85a3:0:0:8a2e:370:7330/124`; `"::1"` prints `0:0:0:0:0:0:0:1/128`; invalid: `""`, `"192.168.1.1/33"`, `"2001:db8:85a3:0:0:8a2e:370:7334/129"`, `"192.168.1.1:"`, `"1.2.3.10000"`; `Equal`: `192.168.1.0/24` equals `192.168.1.1/32` and `2001:db8:85a3:0:0:8a2e:370:7330/124` equals `…:7334/128` (the shift rule); and, mirroring the C++ representation, `ParseMaskedIP("::1")` equals `ParseMaskedIP("0.0.0.1")`.

Operator table, one `DescribeTable` with `(op, env, values, ifexists, isruntime, squidWant, tentacleWant)`:

| case | env | op / values | Squid | Tentacle |
|---|---|---|---|---|
| absent key, StringEquals | {} | `["x"]` | false | false |
| absent, IfExists | {} | `["x"]`, ifexists | true | true |
| absent, ForAllValues:StringEquals | {} | `["x"]` | true | true |
| Null present | {k: a} | `["true"]` | false | false |
| Null absent | {} | `["true"]` | true | true |
| Null absent, values false | {} | `["false"]` | true | false |
| StringEquals | {k: a} | `["a","b"]` | true | true |
| StringNotEquals, one of two env values differs | {k: a, k: b} | `["a"]` | true | false |
| StringNotEquals, all differ | {k: c} | `["a"]` | true | true |
| StringNotEquals, equal | {k: a} | `["a"]` | false | false |
| StringEqualsIgnoreCase | {k: A} | `["a"]` | true | true |
| StringLike | {k: abc} | `["a*"]` | true | true |
| ForAllValues:StringLike, one env value unmatched | {k: ab, k: zz} | `["a*"]` | false | false |
| NumericLessThan | {k: 5} | `["10"]` | true | true |
| NumericNotEquals, one value equal one not | {k: 5} | `["5","6"]` | true | false |
| NumericEquals, unconvertible env | {k: x} | `["5"]` | false | false |
| DateLessThan, epoch vs ISO | {k: 1700000000} | `["2024-01-01T00:00:00Z"]` | true | true |
| DateGreaterThan | {k: 2024-01-01T00:00:00Z} | `["1700000000"]` | true | true |
| Bool | {k: true} | `["true"]` | true | true |
| Bool, number | {k: 1} | `["true"]` | true | true |
| Bool, empty | {k: ""} | `["false"]` | true | true |
| BinaryEquals | {k: aGk=} | `["aGk="]` | true | true |
| IpAddress | {aws:SourceIp: 192.168.1.2} | `["192.168.1.0/24"]` | true | true |
| NotIpAddress, in range | {aws:SourceIp: 192.168.1.1} | `["192.168.1.1/32","2001:0db8:85a3:0000:0000:8a2e:0370:7334"]` | false | false |
| NotIpAddress, out of range | {aws:SourceIp: 192.168.1.2} | same | true | true |
| ArnLike | {aws:SourceArn: arn:aws:s3:::b/k} | `["arn:aws:s3:::b/*"]` | true | true |
| ArnLike, malformed env | {aws:SourceArn: b/k} | `["arn:aws:s3:::b/*"]` | false | false |
| runtime ${s3:ResourceTag/x} | {aws:PrincipalTag/x: v, s3:ResourceTag/x: v} | key `aws:PrincipalTag/x`, `["${s3:ResourceTag/x}"]`, isruntime | true | true |

- [ ] **Step 2: Run to see them fail**; **Step 3: Implement** as specified; **Step 4: Run to see them pass**; **Step 5: Commit**

```sh
git commit -am "feat(policy): evaluate condition operators with Squid and Tentacle semantics"
```

---

### Task 4: `policy`: `Statement`, `Policy`, `Eval`, `IsPublic` with radosgw's cases

**Files:**
- Create: `internal/policy/statement.go`, `internal/policy/policy.go`, `internal/policy/eval_test.go`, `internal/policy/testdata/example1.json` … `example7.json`, `ip_allow.json`, `ip_deny.json`, `ip_full.json` (the texts at [`test_rgw_iam_policy.cc:926-1029`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L926-L1029), [`:1309-1367`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L1309-L1367))

**Interfaces:**
- Consumes: Tasks 1 to 3.
- Produces:

```go
package policy

// Identity is what Statement::eval_principal asks of rgw::auth::Identity
// (rgw_iam_policy.cc:1235-1276): authz implements it over op.Identity.
type Identity interface {
	// IsIdentity is Identity::is_identity for one principal.
	IsIdentity(p Principal) bool
	// IdentityType is get_identity_type: meta.IdentityRole selects the role branch.
	IdentityType() meta.IdentityType
}

type Effect uint8
const (
	Allow Effect = iota // rgw::IAM::Effect order
	Deny
	Pass
)

type Version uint8
const (
	V2008_10_17 Version = iota
	V2012_10_17
)

// Statement is rgw::IAM::Statement (rgw_iam_policy.h:533-559).
type Statement struct {
	Sid          *string
	Principals   []Principal // princ; no duplicates (flat_set)
	NotPrincipals []Principal
	Effect       Effect // Deny when absent, as the C++ default
	Actions      ActionSet
	NotActions   ActionSet
	Resources    []ARN
	NotResources []ARN
	Conditions   []Condition
}

// Eval is Statement::eval (rgw_iam_policy.cc:1192-1233). id may be nil
// (boost::none); res may be nil.
func (s *Statement) Eval(env Env, id Identity, a Action, res *ARN, sem Semantics) Effect
// EvalPrincipal is Statement::eval_principal (:1244-1276).
func (s *Statement) EvalPrincipal(id Identity) Effect
// EvalConditions is Statement::eval_conditions (:1278-1285): Allow when every condition holds, else Deny.
func (s *Statement) EvalConditions(env Env, sem Semantics) Effect

// Policy is rgw::IAM::Policy (rgw_iam_policy.h:581-600).
type Policy struct {
	Text       string
	Version    Version
	ID         *string
	Statements []Statement
}
// Eval is Policy::eval (:1828-1842): any Deny wins, else Allow if any statement allowed, else Pass.
func (p *Policy) Eval(env Env, id Identity, a Action, res *ARN, sem Semantics) Effect
// IsPublic is rgw::IAM::is_public (:1894-1922) under sem.
func (p *Policy) IsPublic(sem Semantics) bool
// HasConditionKeyPrefix is has_partial_conditional: some condition key starts with prefix, case-insensitively.
func (p *Policy) HasConditionKeyPrefix(prefix string) bool
// HasConditionValuePrefix is has_partial_conditional_value.
func (p *Policy) HasConditionValuePrefix(prefix string) bool
```

`Statement.Eval`, in order ([`:1192-1233`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L1192-L1233)): `EvalPrincipal(id) == Deny` → Pass; `res != nil` and both resource lists empty → Pass; `res == nil` and either list non-empty → Pass; resources non-empty and none `Match(*res)` → Pass; else not-resources non-empty and any `Match(*res)` → Pass; `!Actions.Has(a) || NotActions.Has(a)` → Pass; every condition `Eval(env, sem)` → `s.Effect`, else Pass.

`EvalPrincipal` (`:1244-1276`): `id == nil` → Allow. Both lists empty → Deny. Non-role identity: `Principals` non-empty and no `IsIdentity` → Deny. Role identity: `Principals` non-empty → Deny unless some `IsIdentity`; and the `NotPrincipals` check is skipped for roles with a non-empty `Principals` (the `else if` at `:1270`). Otherwise `NotPrincipals` non-empty and some `IsIdentity` → Deny. Else Allow. The `PolicyPrincipal` out-parameter exists for session policies (STS, phase 3) and is not modelled.

`IsPublic` ([`:1894-1922`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L1894-L1922)): with `iamAllEnv := Env{"aws:SourceIp": {"1.1.1.1"}, "aws:UserId": {"anonymous"}, "s3:x-amz-server-side-encryption-aws-kms-key-id": {"secret"}}`, any statement with `Effect == Allow` for which: some principal is a wildcard → `EvalConditions(iamAllEnv, sem) == Allow`; else, on Squid (`!sem.PublicNeedsWildcardPrincipal`), no `NotPrincipals` entry is a wildcard → true; on Tentacle → false.

- [ ] **Step 1: Write the failing specs**

`eval_test.go` builds the statements by hand (Task 5's `Parse` does not exist yet) for the radosgw cases and keeps them as shared fixtures the parse specs reuse:

- `Eval1` ([`:257-276`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L257-L276)): example1's statement (Allow, `S3ListBucket`, resource `arn:aws:s3::arbitrary_tenant:example_bucket`), no identity: `S3ListBucket` on that bucket → Allow; `S3PutBucketAcl` → Pass; `S3ListBucket` on `erroneous_bucket` → Pass.
- `Eval2` ([`:321-357`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L321-L357)): example2 (principal `AccountPrincipal("ACCOUNT-ID-WITHOUT-HYPHENS")`, `s3:*`, resources `mybucket` and `mybucket/*`), a fake identity that matches wildcard or equal principals (the `FakeIdentity` of [`:144-208`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L144-L208)): for every `a < S3All`, Allow on `mybucket` and `mybucket/myobject` for the true account, Pass for another account, Pass on `notyourbucket`.
- `Eval3` (`:475-582`): example3's three statements; `S3PutBucketPolicy` Allow on any ARN; every other action except `S3ListAllMyBuckets` and `S3PutBucketPolicy`: Pass with an empty env, Allow only for the `List*`/`Get*` set under `{aws:MultiFactorAuthPresent: true}` on `confidential-data` and `confidential-data/moo`, Pass under `false`, Pass on `really-confidential-data`. The `s3allow` set is the 35 actions listed at [`:481-516`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L481-L516).
- `Eval4`-`Eval6` ([`:614-677`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L614-L677), [`:709-722`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L709-L722)): iam action and resource matching, including `IAMCreateRole` on `arn:aws:iam:::role/example_role` (account "") → Pass against a statement whose resource account was rewritten to the tenant (`Eval5`, [`:673-676`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L673-L676)), and `*` matching `S3ListBucket` on an iam ARN (`Eval6`).
- `Eval7` ([`:757-782`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L757-L782)): principal `UserPrincipal("", "A:subA")` matches only the identity equal to it.
- `EvalIPAddress` ([`:1202-1307`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L1202-L1307)): the allow, deny and full IP policies against envs with `aws:SourceIp` 192.168.1.2 (allowed), 192.168.1.1 (blocklisted), `::1`, `2001:0db8:85a3:0000:0000:8a2e:0370:7334`; expected effects exactly as the 20 assertions there.
- Deny precedence: two statements Allow then Deny for the same action → Deny; Deny then Allow → Deny.
- `EvalPrincipal` for a non-role identity: `Principals` `[A]` and `NotPrincipals` `[B]` with identity B → Deny; identity A → Allow; role identity with `Principals` `[A]` and `NotPrincipals` `[A]` → Allow (the skipped `else if`).
- `IsPublic`: an Allow statement with `Principal: *` and no conditions → true on both; with `Principal: *` and `IpAddress aws:SourceIp 10.0.0.0/8` → false on both (1.1.1.1 fails it); an Allow statement with `Principal: user/A` and no `NotPrincipal` → true on Squid, false on Tentacle; a Deny-only policy → false.

- [ ] **Step 2: Run to see them fail**; **Step 3: Implement**; **Step 4: Run to see them pass**; **Step 5: Commit**

```sh
git commit -am "feat(policy): evaluate statements and policies with radosgw's precedence"
```

---

### Task 5: `policy`: `Parse`, stored user policies, managed policies

**Files:**
- Create: `internal/policy/parse.go`, `internal/policy/stored.go`, `internal/policy/managed.go`, `internal/policy/parse_test.go`, `internal/policy/stored_test.go`

**Interfaces:**
- Consumes: Tasks 1 to 4; `denc.Decoder`, `denc.DecodeMap`, `denc.DecodeSlice`.
- Produces:

```go
package policy

// ParseOptions are Policy's constructor arguments (rgw_iam_policy.cc:1815-1826).
type ParseOptions struct {
	// Tenant rewrites resource ARNs whose account is "" or "*" to the tenant
	// and rejects other accounts (:665-684); nil is radosgw's nullptr (no
	// rewriting, any account accepted), used for account users' identity
	// policies and for managed policies.
	Tenant *string
	// RejectInvalidPrincipals fails parsing on an unsupported principal (:735-745)
	// instead of logging and dropping it; PutBucketPolicy passes
	// rgw_policy_reject_invalid_principals (default true), stored policies false.
	RejectInvalidPrincipals bool
	// Release selects the action vocabulary (Known) and Semantics.
	Release denc.Release
}

// ParseError is PolicyParseException: "At character offset N, <annotation>".
type ParseError struct {
	Offset     int64
	Annotation string
}
func (e *ParseError) Error() string

// Parse parses an IAM policy document as PolicyParser does. The text is kept
// in Policy.Text exactly as given, because PutBucketPolicy stores p.text.
func Parse(text string, opts ParseOptions) (*Policy, error)

// DecodeUserPolicies decodes RGW_ATTR_USER_POLICY, a std::map<string,string>
// of policy name to text (rgw_auth.cc:85-95), and parses each with tenant and
// RejectInvalidPrincipals false.
func DecodeUserPolicies(b []byte, tenant *string, r denc.Release) ([]*Policy, error)
// DecodeManagedPolicies decodes RGW_ATTR_MANAGED_POLICY (rgw_iam_managed_policy.cc:178-190,
// ENCODE_START(1,1) then a std::set<string> of ARNs) and returns the
// managed policies it names; an unknown ARN is skipped (:97-108).
func DecodeManagedPolicies(b []byte, r denc.Release) ([]*Policy, error)
// ManagedPolicy returns the built-in policy for an AWS managed-policy ARN
// (rgw_iam_managed_policy.cc:156-176), parsed with a nil tenant.
func ManagedPolicy(arn string, r denc.Release) (*Policy, bool)
```

The parser is a state machine over `encoding/json` tokens that mirrors `PolicyParser`/`ParseState` ([`rgw_iam_policy.cc:217-810`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L217-L810)); rapidjson runs with `kParseCommentsFlag | kParseNumbersAsStringsFlag`, so:

- Comments: `stripComments` removes `//…` to end of line and `/*…*/` outside string literals before tokenizing (rapidjson's comment grammar).
- Numbers: `json.Decoder.UseNumber()`; the `json.Number` text is the value (`:762-778`, numbers are legal only as condition values).
- `true`, `false`, `null` anywhere fail (`Default()` returns false, [`:436-438`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L436-L438)): annotation `"literal not allowed"`; radosgw's message for this case is the unhelpful default and is not matched.
- Keyword tables from `rgw_iam_policy_keywords.gperf`: top level `Version` (string, once), `Id` (string, once), `Statement` (object or array of objects, once); statement level `Sid` (string), `Effect` (`Allow`/`Deny`), `Principal` (string `*` or object; **not** an array, [`:230-236`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L230-L236)), `NotPrincipal` (string, object or array), `Action`/`NotAction` (string or array), `Resource`/`NotResource` (string or array), `Condition` (object); principal-type keys `AWS`, `Federated`, `Service`, `CanonicalUser` (string or array); every other key is `Unknown key `<k>`` (`[:481-482](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L481-L482)`).
- The `seen` set ([`:290-372`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L290-L372)): a top-level key twice, a statement-level key twice within one statement, or a principal-type key twice within one statement (the type bits are shared between `Principal` and `NotPrincipal`, so a statement with both `Principal.AWS` and `NotPrincipal.AWS` fails) → `Token `<k>` is not allowed in the context of `<parent>``. Statement-level bits reset when a statement object closes inside the array (`[:448-462](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L448-L462)`, `[:780-782](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L780-L782)`).
- `Version` must be `2008-10-17` or `2012-10-17` ([`:600-609`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L600-L609)); `Effect` must be `Allow` or `Deny` ([`:620-628`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L620-L628)).
- `Principal: "*"` sets a wildcard ([`:629-632`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L629-L632)); any other string under `Principal` is `` `<s>` is not valid in the context of `Principal` `` (`[:748-752](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L748-L752)`).
- Actions (`:633-664`): `*` sets `AllValue`; otherwise `MatchAction(&set, s, opts.Release)`; no match → `` `<s>` is not valid action `` (annotation text `is not a valid action`, `[:757-759](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L757-L759)`).
- Resources ([`:665-684`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L665-L684)): `ParseARN(s, true)` or `` `<s>` is not a valid ARN. Resource ARNs should have a format like `arn:aws:s3::tenant:resource' or `arn:aws:s3:::resource`. ``; then the tenant rule: with `opts.Tenant` set, account `""` or `"*"` becomes the tenant, an account equal to the tenant passes, any other account is `` Policy owned by tenant `<t>` cannot grant access to resource owned by tenant `<a>`. ``; with a nil tenant every account passes unchanged.
- Conditions ([`:464-490`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L464-L490), [`:685-720`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L685-L720)): under `Condition`, a key is an operator name, optionally suffixed `IfExists` ([`:469-476`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L469-L476)); an unknown operator is `Unknown key`; under the operator object, each key is a condition key and its value a string, a number, or an array of them; a value starting with `$` must be `${…}` and marks the condition `IsRuntime` ([`:701-717`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L701-L717)), otherwise `` Invalid interpolation `<s>` ``.
- Principals ([`:521-587`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L521-L587)): under `AWS`: `*` → wildcard; an ARN (no wildcards) whose resource is `root` → `AccountPrincipal(account)`; `user/<name>` → `UserPrincipal`; `role/<name>` → `RolePrincipal`; `oidc-provider/<url>` → `OIDCProviderPrincipal`; `assumed-role/<name>` → `AssumedRolePrincipal`; a string with no `:` and no `/` → `AccountPrincipal(s)` (a bare tenant); anything else is invalid with the long message at [`:571-581`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L571-L581). `Federated` follows the same ARN rules. `CanonicalUser` is always invalid (`RGW does not support canonical users.`). `Service` is `ServicePrincipal(s)` on Tentacle (`sem.ServicePrincipals`) and invalid on Squid. Invalid: `RejectInvalidPrincipals` → `ParseError` with that message; otherwise `slog.Warn("ignored principal", ...)` and continue ([`:735-745`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L735-L745)).
- Tentacle only (`sem.RejectAllowWithNotPrincipal`): after each string value, a statement with `Effect == Allow` and a non-empty `NotPrincipals` is `Allow with NotPrincipal is not allowed.` ([`[T]:771-775`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L771-L775)).
- `Offset` is `dec.InputOffset()` at the failing token, in the comment-stripped text.

`stored.go`: `DecodeUserPolicies` is `denc.DecodeMap(d, (*denc.Decoder).String, (*denc.Decoder).String)` with no struct frame (a bare `std::map` encoding), then `Parse` of each value in key order with `ParseOptions{Tenant: tenant, RejectInvalidPrincipals: false, Release: r}`; a parse failure is returned (radosgw throws out of `load_account_and_policies`, and the request fails). `DecodeManagedPolicies` is `BeginStruct(1)`, `denc.DecodeSlice(d, (*denc.Decoder).String)` (a `std::set` encodes as a count and sorted strings), `EndStruct`. `managed.go` holds the six texts of [`rgw_iam_managed_policy.cc:26-153`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_managed_policy.cc#L26-L153) as raw string constants, byte for byte, under their ARNs `arn:aws:iam::aws:policy/{IAMFullAccess,IAMReadOnlyAccess,AmazonSNSFullAccess,AmazonSNSReadOnlyAccess,AmazonS3FullAccess,AmazonS3ReadOnlyAccess}`.

- [ ] **Step 1: Write the failing specs**

`parse_test.go`, with `tenant := "arbitrary_tenant"` and `opts := policy.ParseOptions{Tenant: &tenant, RejectInvalidPrincipals: true, Release: denc.Squid}`:

- `Parse1`..`Parse7` ([`test_rgw_iam_policy.cc:227-255`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L227-L255), [`:278-319`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L278-L319), [`:359-473`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L359-L473), [`:584-612`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L584-L612), [`:629-657`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L629-L657), [`:679-707`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L679-L707), [`:724-755`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L724-L755)): parse each testdata file and assert the same fields radosgw asserts: `Text` equals the input, `Version`, `ID`, per statement `Sid`, principals, `Effect`, `Actions` (built with `ActionSet.Set`; `Parse5`'s `iam:*` is `IAMAllValue()` plus `IAMAll`; `Parse3`'s third statement is the 35-action set with `S3All` not set), `NotActions` zero, resources (`Parse1`: account rewritten to the tenant; `Parse3`: `*` gives the all-wildcard ARN with account `arbitrary_tenant`), conditions (`Parse3`: `OpBool`, key `aws:MultiFactorAuthPresent`, value `"true"`, not `IfExists`), `Parse7`'s principal `UserPrincipal("", "A:subA")`.
- `ParseIPAddress` ([`:1143-1200`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L1143-L1200)): the two conditions with their value lists.
- Eval through parsed policies: re-run Task 4's `Eval1`, `Eval3` and `EvalIPAddress` expectations on the parsed documents to prove parse and hand-built agree.
- Errors, each `MatchError(&policy.ParseError{})` with the annotation asserted through `errors.As`: `{"Version": "2020-01-01", "Statement": {...}}` → `is not a valid version`; a statement with `"Effect": "Maybe"` → `is not a valid effect`; `"Action": "s3:Nope"` → `is not a valid action`; `"Action": "s3:GetObjectAttributes"` with `Release: denc.Squid` → `is not a valid action`, and with `denc.Tentacle` parses (Review Focus 3); `"Resource": "arn:aws:s3::othertenant:b"` → `cannot grant access to resource owned by tenant`; `"Resource": "bucket"` → `is not a valid ARN`; `"Principal": ["*"]` → `does not take array`; `"Principal": {"CanonicalUser": "abc"}` → `RGW does not support canonical users.` with `RejectInvalidPrincipals`, and parses to a statement with no principals without it; `"Principal": {"AWS": "arn:aws:iam::t:group/g"}` → `is not a supported AWS or Federated ARN`; two `Effect` keys in one statement → `is not allowed in the context of`; `{"Version": "2012-10-17", "Statement": {...}, "Extra": 1}` → `Unknown key`; a `true` literal as a condition value → error; `"Condition": {"StringEquals": {"k": "${broken"}}` → `Invalid interpolation`; `"Condition": {"Nope": {"k": "v"}}` → `Unknown key`; `"Condition": {"StringEqualsIfExists": {"k": "v"}}` parses with `IfExists`; a document with `// comment` and `/* comment */` lines parses; `"Statement": [{"Effect": "Allow", "NotPrincipal": {"AWS": "*"}, "Action": "s3:*", "Resource": "*"}]` parses on Squid and fails on Tentacle with `Allow with NotPrincipal is not allowed.`; `"Principal": {"Service": "s3.amazonaws.com"}` is invalid on Squid and a `ServicePrincipal` on Tentacle; `"Principal": {"AWS": "tenantA"}` (bare tenant) → `AccountPrincipal("tenantA")`; `"Principal": {"AWS": "arn:aws:iam::acct:root"}` → `AccountPrincipal("acct")`.
- Tenant nil: `"Resource": "arn:aws:s3::othertenant:b"` parses with the account kept.

`stored_test.go`: encode a `map<string,string>{"p1": example1, "p2": example4}` with `denc.EncodeMap` and assert `DecodeUserPolicies` returns two policies with `Text` equal to the inputs and `Eval1`'s first expectation holding on the first; a set `{"arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess", "arn:aws:iam::aws:policy/Unknown"}` encoded under `BeginStruct(1,1)` decodes to one policy whose `Statements[0].Actions` is the `s3:Get*`, `s3:List*`, `s3:Describe*`, `s3-object-lambda:*` set (`ManagedPolicyTest.AmazonS3ReadOnlyAccess`, [`test_rgw_iam_policy.cc:872-924`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L872-L924)); `ManagedPolicy("arn:aws:iam::aws:policy/IAMFullAccess", denc.Squid)` has `IAMAllValue()` plus `IAMAll` plus the ten `organizations:` actions ([`:792-801`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L792-L801)); `AmazonSNSReadOnlyAccess` has `SNSGetTopicAttributes` and `SNSListTopics` ([`:847-859`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L847-L859)).

- [ ] **Step 2: Run to see them fail**; **Step 3: Implement**; **Step 4: Run to see them pass**

Run: `go test -tags=ceph_preview ./internal/policy/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git commit -am "feat(policy): parse IAM policy documents as radosgw does and decode stored user and managed policies"
```

Open the draft PR `feat(policy): IAM policy language (unit Z tasks 1-5)`.

---

### Task 6: `acl`: evaluation, `PermFor`, defaults, `PublicAccessBlock`

**Files:**
- Create: `internal/acl/perm.go`, `internal/acl/eval.go`, `internal/acl/publicaccess.go`, `internal/acl/eval_test.go`, `internal/acl/publicaccess_test.go`
- Modify: `internal/acl/doc.go` (drop "Grant evaluation, canned ACLs and the S3 and Swift renderings live elsewhere")

**Interfaces:**
- Consumes: phase 0's `acl` types; `policy.Action`; `meta.Owner`, `meta.ParseOwner`.
- Produces:

```go
package acl

// The Swift bits and the invalid marker of rgw_acl_types.h:35-40.
const (
	PermReadObjs  Permission = 0x10
	PermWriteObjs Permission = 0x20
	PermInvalid   Permission = 0xFF00
)

// PermFor is rgw::IAM::op_to_perm (rgw_iam_policy.h:237-318): the ACL
// permission an action needs when no policy decides; PermInvalid for an
// action with no ACL equivalent (nothing then matches, as in radosgw).
func PermFor(a policy.Action) Permission

// Identity is what RGWAccessControlPolicy::get_perm asks of
// rgw::auth::Identity (rgw_acl.cc:111-119, :160-199); authz implements it.
type Identity interface {
	// IsOwnerOf is is_owner_of for an ACLOwner id in its string form.
	IsOwnerOf(ownerID string) bool
	// PermsFromACLSpec is get_perms_from_aclspec over the list's user map.
	PermsFromACLSpec(userMap map[string]int32) Permission
	// IsAnonymous is is_anonymous.
	IsAnonymous() bool
}

// Perm is RGWAccessControlPolicy::get_perm (rgw_acl.cc:160-199): the user
// map through id, READ_ACP|WRITE_ACP for the owner, the AllUsers group and,
// for a non-anonymous identity, AuthenticatedUsers unless ignorePublicACLs,
// then the referer list when referer is non-empty (nullptr is "").
func (p Policy) Perm(id Identity, mask Permission, referer string, ignorePublicACLs bool) Permission

// Verify is RGWAccessControlPolicy::verify_permission (:201-233): perm is
// granted when perm | READ_OBJS | WRITE_OBJS through Perm, with WRITE_OBJS
// implying WRITE|WRITE_ACP and READ_OBJS implying READ|READ_ACP, masked by
// userPermMask, equals perm.
func (p Policy) Verify(id Identity, userPermMask, perm Permission, referer string, ignorePublicACLs bool) bool

// IsPublic is RGWAccessControlPolicy::is_public (:235-247): AllUsers or
// AuthenticatedUsers holds any bit of FULL_CONTROL.
func (p Policy) IsPublic() bool

// DefaultPolicy is create_default (rgw_acl.h:341-349, :430-433): the owner
// with one FULL_CONTROL grant to itself.
func DefaultPolicy(owner meta.Owner, displayName string) Policy

// AddGrant is RGWAccessControlList::add_grant (rgw_acl.cc:92-102): appends
// to Grants under the grantee's key (the user id or the email address, ""
// for groups and referers) and registers it in the maps (:71-90).
func (l *List) AddGrant(g Grant)
// RemoveCanonUserGrant is remove_canon_user_grant (:104-109).
func (l *List) RemoveCanonUserGrant(ownerID string)
// GroupPerm is get_group_perm (:121-135).
func (l List) GroupPerm(group uint32, mask Permission) Permission

// PublicAccessBlock is PublicAccessBlockConfiguration (rgw_public_access.h:21-70),
// the user.rgw.public-access bucket attr. Managing it is not implemented
// yet; the stored value is honoured here.
type PublicAccessBlock struct {
	BlockPublicACLs       bool
	IgnorePublicACLs      bool
	BlockPublicPolicy     bool
	RestrictPublicBuckets bool
}
func (b PublicAccessBlock) Encode(e *denc.Encoder, _ denc.Release) // ENCODE_START(1,1) + 4 bools
func DecodePublicAccessBlock(d *denc.Decoder) PublicAccessBlock    // DECODE_START(1)
```

`Perm` transcribed: `perm := mask & id.PermsFromACLSpec(l.UserMap)`; `if id.IsOwnerOf(p.Owner.ID) { perm |= mask & (PermReadACP | PermWriteACP) }`; `if perm == mask { return perm }`; `if !ignorePublicACLs && perm&mask != mask { perm |= l.GroupPerm(GroupAllUsers, mask); if !id.IsAnonymous() { perm |= l.GroupPerm(GroupAuthenticatedUsers, mask) } }`; `if referer != "" && perm&mask != mask { perm = l.RefererPerm(perm, referer, mask) }` where `RefererPerm` ([`:137-158`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L137-L158)) walks every `Referer` in list order and takes the last matching `Perm`, then masks. `Referer.IsMatch` ([`rgw_acl.h:212-233`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.h#L212-L233)): the referer's host (`get_http_host`: strip the scheme through `://`, cut at the first `/`, `:` and `@` handling as `rgw_acl.h`'s helper does — read it in the checkout while implementing, it is fifteen lines) must be at least as long as the spec; `*` matches; equality matches; a spec starting with `.` matches a host ending with the spec. Note `is_owner_of(rgw_user(RGW_USER_ANON_ID))` at [`:184`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L184) is the identity's `IsAnonymous`.

`PermFor` is the switch at [`rgw_iam_policy.h:237-318`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L237-L318) verbatim: READ for the object and list gets, WRITE for the object writes and deletes and `s3CreateBucket`/`s3DeleteBucket`, READ_ACP for the bucket-configuration gets and object ACL gets, WRITE_ACP for the bucket-configuration puts and object ACL puts, FULL_CONTROL for `S3All`, `PermInvalid` otherwise (including every non-s3 action and the Tentacle-only s3 actions, which the switch does not list at either release).

- [ ] **Step 1: Write the failing specs**

`eval_test.go` with a `fakeIdentity{owner string; uid string; account string; anon bool}` implementing `Identity` the way `LocalApplier` does (user id lookup plus account id lookup, `IsOwnerOf` by string equality):

- `PermFor`: `S3GetObject` READ, `S3PutObject` WRITE, `S3GetBucketAcl` READ_ACP, `S3PutBucketPolicy` WRITE_ACP, `S3ListBucket` READ, `S3DeleteBucket` WRITE, `S3All` FULL_CONTROL, `IAMCreateRole` `PermInvalid`, `S3GetObjectAttributes` `PermInvalid`.
- `DefaultPolicy(meta.UserOwner(meta.ParseUserID("t$alice")), "Alice")`: owner `t$alice`/`Alice`, one grant `CanonUser t$alice FULL_CONTROL`, `UserMap["t$alice"] == 15`.
- `AddGrant` of a group grant keys it under `""` and updates `GroupMap`; of an email grant keys it under the address; `RemoveCanonUserGrant` drops both the grant and the map entry.
- `Verify`: owner with FULL_CONTROL: READ true, WRITE_ACP true; a stranger: READ false; AllUsers READ grant: stranger READ true, anonymous READ true, anonymous WRITE false; AuthenticatedUsers READ: stranger true, anonymous false; `ignorePublicACLs`: both groups ignored, stranger false; the owner always gets READ_ACP and WRITE_ACP even with no grants ([`:174-176`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L174-L176)); `userPermMask` READ with an owner FULL_CONTROL policy: `Verify(owner, PermRead, PermWrite, ...)` false (Review Focus 5); a `WRITE_OBJS` grant to a user: `Verify(user, FULL_CONTROL, PermWrite)` true and `PermWriteACP` true ([`:213-218`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L213-L218)); a `READ_OBJS` grant: READ and READ_ACP true; referer list `[{".example.com", READ}]` and referer `https://a.example.com/x`: stranger READ true; referer `https://example.org/` false; a wildcard referer grant registered `GroupAllUsers` (`register_grant`, [`:83-87`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L83-L87)) so it also grants without a referer; an account identity matches a grant to its account id ([`rgw_auth.cc:1032-1046`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1032-L1046)).
- `IsPublic`: AllUsers READ → true; AuthenticatedUsers WRITE → true; only user grants → false; a group grant with perm 0 → false.

`publicaccess_test.go`: a hand-built buffer `BeginStruct(1,1)` + bools `true,false,true,false` decodes to `{true,false,true,false}` and re-encodes to the same bytes; the zero value round-trips; the JSON form is not needed.

- [ ] **Step 2: Run to see them fail**; **Step 3: Implement**; **Step 4: Run to see them pass**

Run: `go test -tags=ceph_preview ./internal/acl/...`
Expected: PASS, and the phase-0 golden specs still pass.

- [ ] **Step 5: Commit**

```sh
git commit -am "feat(acl): evaluate grants as RGWAccessControlPolicy does and read the public-access block"
```

---

### Task 7: `acl`: S3 XML, canned ACLs, grant headers, `Resolver`

**Files:**
- Create: `internal/acl/s3xml.go`, `internal/acl/s3xml_test.go`
- Modify: `docs/exclusions.md` (coexistence section: the escaping difference of D-Z7, Step 6)

**Interfaces:**
- Consumes: Task 6; G's `xmltext.Escape` (G Task 4: ceph's XML text escaping, a leaf package).
- Produces:

```go
package acl

// The group URIs of rgw_acl_s3.cc:20-21.
const (
	URIAllUsers           = "http://acs.amazonaws.com/groups/global/AllUsers"
	URIAuthenticatedUsers = "http://acs.amazonaws.com/groups/global/AuthenticatedUsers"
)
// GroupForURI is acl_uri_to_group (:585-593); GroupNone for anything else.
func GroupForURI(uri string) uint32
// URIForGroup is acl_group_to_uri (:595-607).
func URIForGroup(group uint32) (string, bool)

// Resolver looks grantees up; authz.UserResolver implements it over op.UserStore.
type Resolver interface {
	// OwnerByEmail is load_owner_by_email plus read_owner_display_name
	// (:325-337); ErrUnresolvableEmail when no user has the address.
	OwnerByEmail(ctx context.Context, email string) (Owner, error)
	// DisplayName is read_owner_display_name (:300-323) for a user or account
	// owner; ErrNoSuchOwner when it does not exist.
	DisplayName(ctx context.Context, owner meta.Owner) (string, error)
}

var (
	ErrInvalid           = errors.New("acl: invalid")                 // -EINVAL
	ErrUnresolvableEmail = errors.New("acl: unresolvable email")       // -ERR_UNRESOLVABLE_EMAIL
	ErrNoSuchOwner       = errors.New("acl: no such owner")            // a lookup miss; parse_policy turns it into ErrInvalid "Invalid Owner ID"
)

// ParseS3XML is rgw::s3::parse_policy (:609-670): the AccessControlPolicy
// document with its Owner (resolved and required to exist) and Grants (each
// resolved through r). err carries radosgw's err_msg through ParseError.
func ParseS3XML(ctx context.Context, r Resolver, doc []byte) (Policy, error)

// ParseError is a *ParseS3XML failure with the message radosgw sets in
// s->err.message; it wraps ErrInvalid or ErrUnresolvableEmail.
type ParseError struct {
	Message string
	Err     error
}

// MarshalS3XML is rgw::s3::write_policy_xml (:672-676, :475-481): the
// document radosgw writes, with text escaped where radosgw's to_xml writes
// names and ids raw (rgw_acl_s3.cc:172-180).
func (p Policy) MarshalS3XML() []byte

// Canned is rgw::s3::create_canned_acl (:678-689) over create_canned
// (:407-461). An anonymous owner makes the bucket owner the policy owner.
func Canned(owner, bucketOwner Owner, canned string) (Policy, error)

// GrantHeaders are the x-amz-grant-* header values as create_policy_from_headers
// reads them (:483-490, :691-709): each is a comma-separated list of
// `id="..."`, `emailAddress="..."` or `uri="..."` (:339-405).
type GrantHeaders struct {
	Read, Write, ReadACP, WriteACP, FullControl string
}
// FromHeaders is create_policy_from_headers: owner, then the grants in the
// header order READ, WRITE, READ_ACP, WRITE_ACP, FULL_CONTROL.
func FromHeaders(ctx context.Context, r Resolver, owner Owner, h GrantHeaders) (Policy, error)
```

Parsing rules (`:53-72`, `:154-170`, [`:197-244`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L197-L244), [`:463-473`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L463-L473), [`:609-670`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L609-L670)): the root must be `AccessControlPolicy` (any namespace) with `Owner` and `AccessControlList` children; `Owner/ID` is mandatory, `DisplayName` optional; the owner is `meta.ParseOwner(id)` and must resolve (`DisplayName` lookup), else `ParseError{"Invalid Owner ID", ErrInvalid}`; a display name in the document overrides the resolved one when non-empty ([`:641-643`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L641-L643)). Each `Grant` needs a `Grantee` with an attribute whose local name is `type` (radosgw reads the literal `xsi:type`; accept the local name under any prefix) and a `Permission` child: only the first `Permission` element counts (`find_first`, [`:213`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl_s3.cc#L213)) and its text is one of `READ`, `WRITE`, `READ_ACP`, `WRITE_ACP`, `FULL_CONTROL` case-insensitively, else the parse fails with `ErrInvalid`. Grantee types: `CanonicalUser` needs `ID` (resolve display name; failure → `"Invalid CanonicalUser id"`), `Group` needs `URI` (unknown → `"Invalid group uri"`), `AmazonCustomerByEmail` needs `EmailAddress` (miss → `ErrUnresolvableEmail` with `"The e-mail address you provided does not match any account on record."`); any other type or a missing element fails (`"Invalid Grantee type"` / `ErrInvalid`). Grants are added with `AddGrant` in document order. Use `encoding/xml` with a custom `UnmarshalXML` on the grantee to read the attribute by local name; unknown elements are ignored, as expat's `RGWXMLParser` ignores them.

Rendering (`:36-51`, `:172-180`, [`:246-280`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl_s3.cc#L246-L280), [`:286-292`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl_s3.cc#L286-L292), [`:475-481`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl_s3.cc#L475-L481)): `<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`, then `<Owner><ID>…</ID>[<DisplayName>…</DisplayName>]</Owner>` only when the owner id is non-empty, then `<AccessControlList>` with one `<Grant>` per entry of `sortedGrants()` whose permission has any S3 bit: `<Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser|AmazonCustomerByEmail|Group|unknown">` with `<ID>` and optional `<DisplayName>`, or `<EmailAddress>`, or `<URI>`, or nothing for `unknown` (a referer grant seen through S3), `</Grantee>`, then the permission: one `<Permission>FULL_CONTROL</Permission>` when all four bits are set, else one element per set bit in the order READ, WRITE, READ_ACP, WRITE_ACP; `</Grant>`; `</AccessControlList></AccessControlPolicy>`. No XML declaration (the s3 layer's `WriteXML` adds it), no whitespace. Every text node through `xmltext.Escape` (G Task 4), never `xml.EscapeText`, whose `&#34;`, `&#39;` and U+FFFD replacement are not radosgw's.

Canned ([`:407-461`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl_s3.cc#L407-L461)): the owner gets FULL_CONTROL first; `""` and `private` add nothing; `public-read` adds AllUsers READ; `public-read-write` adds AllUsers READ and AllUsers WRITE as two grants; `authenticated-read` adds AuthenticatedUsers READ; `bucket-owner-read` and `bucket-owner-full-control` add the bucket owner READ or FULL_CONTROL only when the bucket owner differs from the owner; anything else is `ErrInvalid`. `Canned` sets the policy owner to `bucketOwner` when `owner.ID` parses to the anonymous user ([`:682-686`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl_s3.cc#L682-L686)).

Headers ([`:339-405`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl_s3.cc#L339-L405)): each header splits on `,`; each item is `key=value` (`parse_key_value`, the first `=`), the value with surrounding quotes trimmed (`rgw_trim_quotes`); `emailAddress` resolves through `OwnerByEmail`; `id` is `meta.ParseOwner` plus `DisplayName`; `uri` through `GroupForURI` (`GroupNone` → `ErrInvalid`); any other key → `ErrInvalid`; keys compare case-insensitively.

- [ ] **Step 1: Write the failing specs**

`s3xml_test.go` with a `mapResolver{names map[string]string; emails map[string]meta.Owner}`:

- Round trip: `DefaultPolicy(alice, "Alice").MarshalS3XML()` equals exactly `<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner><AccessControlList><Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>alice</ID><DisplayName>Alice</DisplayName></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`, and `ParseS3XML` of it gives an equal `Policy`.
- A READ|WRITE grant renders two `Permission` elements in that order; a group grant renders `<URI>`; an email grantee parses to a canonical-user grant (email grants are stored as canonical users after resolution, `:504-511`); a referer grant renders as `xsi:type="unknown"` with an empty grantee; a display name `A & B <x> 'q'` renders as `<DisplayName>A &amp; B &lt;x&gt; &apos;q&apos;</DisplayName>` and parses back.
- Parse failures: no `Owner` → `"Missing element Owner"`; owner unknown to the resolver → `"Invalid Owner ID"`; grant without `Permission` → `ErrInvalid`; `Permission` `read` (lowercase) parses (strcasecmp); `Permission` `NOPE` → `ErrInvalid`; `xsi:type="Nope"` → `ErrInvalid`; two `Permission` elements `READ` then `WRITE` → only READ; grantee email not in the resolver → `ErrUnresolvableEmail` with the exact message; a document with a `<Grantee type="CanonicalUser">` (no prefix) parses; a document with the elements in the S3 namespace parses.
- `Canned`: each of the six names with owner `alice` and bucket owner `bob`; `bucket-owner-read` with owner == bucket owner adds nothing; `Canned(anonymousOwner, bob, "")` has owner `bob`; `"nope"` → `ErrInvalid`.
- `FromHeaders`: `Read: "id=\"bob\", uri=\"http://acs.amazonaws.com/groups/global/AllUsers\""`, `FullControl: "emailAddress=\"c@x\""` gives three grants with the resolved names; `Read: "nope=\"x\""` → `ErrInvalid`; an unknown `id` → `ErrNoSuchOwner` wrapped in `ErrInvalid`? No: `parse_grantee_str` returns the lookup's error (`:369-372`), which is `-ENOENT`; `FromHeaders` returns `ErrNoSuchOwner` and `authz.ErrorFor` maps it (Task 11).

- [ ] **Step 2: Run to see them fail**; **Step 3: Implement**; **Step 4: Run to see them pass**; **Step 5: Commit**

```sh
git commit -am "feat(acl): parse and render the S3 ACL document, canned ACLs and grant headers"
```

- [ ] **Step 6: Record the difference in `docs/exclusions.md`; commit**

`MarshalS3XML` escapes the text radosgw writes raw, so this task records the difference (D-Z7) in the PR that introduces it. In `docs/exclusions.md`, "Coexistence obligations independent of any exclusion", append:

```markdown
- **ACL documents escape what radosgw writes raw.** radosgw writes the
  owner's and each grantee's ID and display name into the GetBucketAcl and
  GetObjectAcl documents without XML escaping (`rgw_acl_s3.cc:172-180` and
  `:246-280` at v19.2.6 and v20.2.4), so a display name containing `&` or
  `<` yields a document no XML parser accepts. rgw-go escapes that text as
  radosgw's XML formatter escapes the text of its other documents
  (`xml_stream_escaper`, `src/common/escape.cc:134-169`). The stored ACL is the same either way; only the rendered
  document differs, and only for such names.
```

```sh
git add docs/exclusions.md
git commit -m "docs(exclusions): record that ACL documents escape owner and grantee names"
```

Open the draft PR `feat(acl): ACL evaluation and the S3 ACL subresource (unit Z tasks 6-7)`; its description names the exclusions entry, which is reported for the rgw-rs session.

---

### Task 8: `tags`: `RGWObjTags`, XML, header

**Files:**
- Create: `internal/tags/doc.go`, `internal/tags/tags.go`, `internal/tags/xml.go`, `internal/tags/tags_suite_test.go`, `internal/tags/tags_test.go`, `internal/tags/goldens_test.go`, `internal/tags/testdata/goldens/RGWObjTags/…` (generated)
- Modify: `hack/goldens/types.txt` (append `RGWObjTags internal/tags`)

**Interfaces:**
- Consumes: `denc`; G's `xmltext.Escape` (G Task 4).
- Produces:

```go
package tags

// The limits of rgw_tag.h:18-20 and RGWPutBucketTags_ObjStore_S3::get_params (rgw_rest_s3.cc:903).
const (
	MaxObjectTags = 10
	MaxBucketTags = 50
	MaxKeyLen     = 128
	MaxValueLen   = 256
)

// Tag is one entry of RGWObjTags' std::multimap<string,string>.
type Tag struct{ Key, Value string }

// Set is RGWObjTags. Tags is kept in multimap order: sorted by key, equal
// keys in insertion order.
type Set struct {
	Tags []Tag
}

var ErrInvalidTag = errors.New("tags: invalid tag") // -ERR_INVALID_TAG

// Add is check_and_add_tag (rgw_tag.cc:23-34) with max as max_obj_tags: an
// empty key, a key over MaxKeyLen, a value over MaxValueLen or a set already
// holding max tags is ErrInvalidTag.
func (s *Set) Add(key, value string, max int) error
func (s Set) Len() int

// Encode is RGWObjTags::encode (rgw_tag.h:27-31): ENCODE_START(1,1) then the multimap.
func (s Set) Encode(e *denc.Encoder, _ denc.Release)
// Decode is RGWObjTags::decode (:33-63): DECODE_START_LEGACY_COMPAT_LEN(1,1,1)
// and the multimap; when that fails, the whole buffer minus trailing NULs is
// read as the URL-encoded "k=v&k2=v2" form through ParseHeader with
// MaxObjectTags; an empty or unparsable fallback fails with the decoder error.
func Decode(d *denc.Decoder) Set
// MarshalJSON is RGWObjTags::dump (rgw_tag.cc:59-66): {"tagset": {key: value, ...}}.
func (s Set) MarshalJSON() ([]byte, error)

// ParseHeader is set_from_string (rgw_tag.cc:36-57): the x-amz-tagging
// header, "&"-separated "key=value" items, each side URL-decoded, a bare
// key an empty value, each through Add with max.
func ParseHeader(h string, max int) (Set, error)

var ErrMalformedXML = errors.New("tags: malformed xml") // -ERR_MALFORMED_XML

// ParseXML is RGWObjTagging_S3::decode_xml plus rebuild (rgw_tag_s3.cc:14-17,
// :32-57): <Tagging><TagSet><Tag><Key/><Value/></Tag>*</TagSet></Tagging>,
// TagSet mandatory, Tag optional, Key and Value both mandatory in each Tag.
// A structural failure is ErrMalformedXML; a limit failure ErrInvalidTag.
func ParseXML(doc []byte, max int) (Set, error)
// MarshalXML is the body RGWGetObjTags_ObjStore_S3::send_response_data writes
// (rgw_rest_s3.cc:754-771): <Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet>
// then one <Tag><Key>k</Key><Value>v</Value></Tag> per tag, then </TagSet></Tagging>.
// Keys and values go through xmltext.Escape: dump_xml writes them with
// encode_xml, which is the formatter's dump_string (rgw_tag_s3.cc:59-64,
// rgw_xml.cc:448-451).
func (s Set) MarshalXML() []byte
```

The object attr is `user.rgw.x-amz-tagging` (`RGW_ATTR_TAGS`, [`rgw_common.h:115`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L115)); `meta` gains no constant for it here, `authz` and the ops use `tags.Attr = meta.AttrPrefix + "x-amz-tagging"` exported from `tags`.

- [ ] **Step 1: Generate the goldens**

Append `RGWObjTags internal/tags` to `hack/goldens/types.txt`, create `internal/tags/doc.go` so the directory exists, and run `make goldens` (podman; the corpus at `/home/jhoblitt/github/ceph/ceph-object-corpus/archive/*/objects/RGWObjTags` exists and ceph-dencoder registers the type, [`src/tools/ceph-dencoder/rgw_types.h:220`](https://github.com/ceph/ceph/blob/v19.2.6/src/tools/ceph-dencoder/rgw_types.h#L220)). Commit the generated files.

- [ ] **Step 2: Write the failing specs**

`goldens_test.go`: `goldentest.RoundTrip("testdata", "RGWObjTags", squid, tags.Decode, encode)` with `SkipJSON: false` (the dump is `{"tagset": {...}}`). `tags_test.go`: `Add` enforces each limit with `ErrInvalidTag` (empty key; 129-byte key; 257-byte value; an eleventh tag at `MaxObjectTags`; a 51st at `MaxBucketTags`); duplicate keys are kept in insertion order and encode after sorting by key; `Decode` of a hand-built `ENCODE_START(1,1)` buffer with two tags; `Decode` of the raw bytes `k1=v1&k2=v%202` (no frame) gives two tags with `v 2`; `Decode` of `k=v\x00\x00` strips the NULs; `Decode` of an empty buffer fails; `ParseHeader("a=1&b&c=%26", 10)` gives `a=1`, `b=`, `c=&`; `ParseHeader` of 11 items with `MaxObjectTags` → `ErrInvalidTag`; `ParseXML` of `<Tagging xmlns="…"><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>` gives one tag; an empty `<TagSet/>` gives an empty set (a tag set may be empty); a `Tag` without `Value` → `ErrMalformedXML`; no `TagSet` → `ErrMalformedXML`; not XML → `ErrMalformedXML`; 11 tags with `MaxObjectTags` → `ErrInvalidTag`, 11 with `MaxBucketTags` ok; `MarshalXML` of `{k: v}` is exactly `<Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>` and of the empty set `<Tagging xmlns="…"><TagSet></TagSet></Tagging>`; a value `a<b&c'` renders as `<Value>a&lt;b&amp;c&apos;</Value>`.

- [ ] **Step 3: Run to see them fail**; **Step 4: Implement**; **Step 5: Run to see them pass**

Run: `go test -tags=ceph_preview ./internal/tags/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add internal/tags hack/goldens/types.txt
git commit -m "feat(tags): add RGWObjTags with corpus goldens, the tagging XML and the x-amz-tagging header"
```

Open the draft PR `feat(tags): object and bucket tag sets (unit Z task 8)`.

---

### Task 9: `authz`: identity view, environment builder, loaders

**Files:**
- Create: `internal/authz/doc.go`, `internal/authz/config.go`, `internal/authz/identity.go`, `internal/authz/env.go`, `internal/authz/load.go`, `internal/authz/authz_suite_test.go`, `internal/authz/identity_test.go`, `internal/authz/env_test.go`, `internal/authz/load_test.go`

**Interfaces:**
- Consumes: `op.Request`, `op.Identity`, `op.BucketRecord`, `op.ObjectState`, `op.Env.Zone.Release()`, `cephconf.Options`; Tasks 5, 6, 8.
- Produces:

```go
package authz

// Config is what the authorizer reads from Ceph configuration.
type Config struct {
	Release denc.Release
	// RejectInvalidPrincipals is rgw_policy_reject_invalid_principals (default true).
	RejectInvalidPrincipals bool
	// EnforceSwiftACLs is rgw_enforce_swift_acls (default true).
	EnforceSwiftACLs bool
	// RemoteAddrParam is rgw_remote_addr_param (default "REMOTE_ADDR"): the
	// CGI-style name of the header carrying the client address.
	RemoteAddrParam string
	// TrustForwardedHTTPS is rgw_trust_forwarded_https (default false).
	TrustForwardedHTTPS bool
	// ACLGrantsMaxNum is rgw_acl_grants_max_num (default 100; negative means 100).
	ACLGrantsMaxNum int
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}
// ConfigFrom reads the options above through o for release r.
func ConfigFrom(o *cephconf.Options, r denc.Release) (Config, error)

// identity is the rgw::auth::LocalApplier view of an op.Identity; it
// implements acl.Identity and policy.Identity (unexported, tested through
// exported helpers below).
type identity struct{ id *op.Identity }
// IsOwnerOf is match_owner (rgw_auth.cc:67-75).
func (v identity) IsOwnerOf(ownerID string) bool
func (v identity) IsOwnerOfOwner(o meta.Owner) bool
// PermsFromACLSpec is LocalApplier::get_perms_from_aclspec (:1032-1046).
func (v identity) PermsFromACLSpec(m map[string]int32) acl.Permission
// IsAnonymous is Identity::is_anonymous (rgw_auth.h:65-70).
func (v identity) IsAnonymous() bool
// IsIdentity is LocalApplier::is_identity (:1058-1076).
func (v identity) IsIdentity(p policy.Principal) bool
// IdentityType is User.Type; meta.IdentityRoot marks an account root.
func (v identity) IdentityType() meta.IdentityType
// PermMask is LocalApplier::get_perm_mask (:1086-1102).
func (v identity) PermMask() acl.Permission
// ACLOwner is get_aclowner (:1019-1030).
func (v identity) ACLOwner() acl.Owner
// IsAdmin is r.Identity.Admin; IsSystem is r.Identity.System.

// PolicyTenant is the tenant identity policies are parsed with
// (rgw_auth.cc:158-160): the user's tenant, or nil for an account user.
func PolicyTenant(id *op.Identity) *string

// BuildEnv is rgw_build_iam_environment (rgw_op.cc:848-910) plus the keys the
// op for action a adds before verify_permission (the table below).
func BuildEnv(cfg Config, r *op.Request, a policy.Action) policy.Env

// The loaders; each reads from the records already on r.
func bucketACL(rec *op.BucketRecord) (acl.Policy, error)           // rgw_op_get_bucket_policy_from_attr (:267-286): default for the bucket owner when absent
func objectACL(st *op.ObjectState, bucketOwner acl.Owner) (acl.Policy, error) // get_obj_policy_from_attr (:288-326): default for the bucket owner when absent
func bucketPolicy(rec *op.BucketRecord, tenant string, r denc.Release) (*policy.Policy, error) // get_iam_policy_from_attr (:329-338)
func identityPolicies(id *op.Identity, r denc.Release) ([]*policy.Policy, error) // load_account_and_policies (rgw_auth.cc:158-171)
func publicAccess(rec *op.BucketRecord) *acl.PublicAccessBlock    // get_public_access_conf_from_attr (:340-355): nil when absent or undecodable
func addTags(env policy.Env, attrs map[string][]byte, existing, resource bool) error // rgw_iam_add_tags_from_bl (:708-725)
func needsTags(policies ...*policy.Policy) (existing, resource bool)  // rgw_check_policy_condition (:777-819)
```

`identity`: `IsOwnerOf(ownerID)` parses the id with `meta.ParseOwner` and applies `match_owner`: a user owner equals `id.User.UserID` (tenant, id and namespace); an account owner equals `id.Account.ID` when `id.Account != nil`. `IsAnonymous` is `IsOwnerOf("anonymous")`, which is `Identity.Anonymous` for G's `op.Anonymous()`. `PermsFromACLSpec`: `m[User.UserID.String()]` or-ed with `m[Account.ID]` when an account is set (`:1032-1046`). `IsIdentity`: wildcard → true; account principal → `(Account != nil && Account.ID == p.Account) || User.UserID.Tenant == p.Account` (`match_account_or_tenant`, [`:77-83`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L77-L83)); user principal → when `Account != nil && p.Account == Account.ID`, `matchPrincipal(User.Path, User.DisplayName, id.SubUser, p.ID)`, else `p.Account == User.UserID.Tenant && matchPrincipal(User.Path, User.UserID.ID, id.SubUser, p.ID)`; every other kind → false ([`:1058-1076`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1058-L1076)). `matchPrincipal` ([`:31-65`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L31-L65)): strip one leading `/` from the path; `expected` must start with the path, then the name; an exact match is true; otherwise the rest must be `:` plus a non-empty string equal to `*` or the subuser. `PermMask`: `SubUser == ""` → `PermFullControl`; else `User.SubUsers[SubUser].Perm` cast, missing → `PermNone` ([`:1086-1102`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1086-L1102)). `IdentityType`: `User.Type`.

`BuildEnv`, base keys ([`rgw_op.cc:848-910`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L848-L910)), `now := cfg.Now()`:

| key | value |
|---|---|
| `aws:CurrentTime` | `strconv.FormatInt(now.Unix(), 10)` (decision D-Z7) |
| `aws:EpochTime` | `now.UTC().Format("2006-01-02T15:04:05.000000000Z")` (`ceph::to_iso_8601` YMDhmsn) |
| `aws:PrincipalType` | `User` |
| `aws:Referer` | `r.Referer` when non-empty |
| `aws:SecureTransport` | `true` when `r.TLS`, or `cfg.TrustForwardedHTTPS` and (`Forwarded` contains `proto=https` or `X-Forwarded-Proto` is `https`) ([`rgw_common.cc:1071-1090`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1071-L1090)); absent otherwise |
| `aws:SourceIp` | when `cfg.RemoteAddrParam` is `REMOTE_ADDR` or empty: the host of `r.RemoteAddr` (`net.SplitHostPort`, brackets removed); otherwise the header named by the parameter converted from CGI form (`HTTP_X_FORWARDED_FOR` → `X-Forwarded-For`): its value, cut at the first `,` when the parameter is exactly `HTTP_X_FORWARDED_FOR`; absent when the header is absent ([`:871-887`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L871-L887)) |
| `aws:UserAgent` | `User-Agent` when present |
| `aws:username` | `r.Identity.User.UserID.ID` (the id without tenant) |
| `rgw:subuser` | `r.Identity.SubUser` (present even when empty) |
| `sts:authentication` | `true` when `X-Amz-Security-Token` is present, else `false` |

Per-action keys, each from the cited op:

| when | keys |
|---|---|
| `a` is `S3ListBucket` or `S3ListBucketVersions` ([`:3033-3045`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3033-L3045)), and for the missing-object check in Task 10 | `s3:prefix` = `prefix` query when non-empty; `s3:delimiter` when non-empty; `s3:max-keys` = the `max-keys` query when it parses as an int, else `1000` |
| `a` is `S3PutObject` and the request is not multipart (`uploads` and `uploadId` absent) and has no `X-Amz-Copy-Source` (RGWPutObj, [`:3965-3975`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3965-L3975)) | `s3:x-amz-acl` = `X-Amz-Acl` value, always added (empty when absent, `rgw_add_to_iam_environment` adds any non-empty key); `s3:x-amz-grant-read`, `-write`, `-read-acp`, `-write-acp`, `-full-control` = the present `X-Amz-Grant-*` headers, only when at least one `X-Amz-Grant-*` header is present (`has_acl_header`, [`rgw_rest_s3.cc:5024`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5024); [`:827-846`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L827-L846)); `s3:RequestObjectTag/<k>` = each tag of `X-Amz-Tagging` parsed leniently (`tags.ParseHeader`, errors ignored here, the op rejects them); `s3:x-amz-server-side-encryption` and `s3:x-amz-server-side-encryption-aws-kms-key-id` from those headers ([`:760-775`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L760-L775)) |
| `a` is `S3PutObject` with `X-Amz-Copy-Source` and no `uploadId` (RGWCopyObj, [`:5482-5485`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5482-L5485)) | `s3:x-amz-copy-source` = the header; `s3:x-amz-metadata-directive` when present |
| `a` is `S3PutObject` with `uploads` (InitMultipart, `:6278`) or `uploadId` on POST (CompleteMultipart, `:6352`) | the two encryption keys only |
| `a` is `S3PutBucketAcl`, `S3PutObjectAcl` or `S3PutObjectVersionAcl` (RGWPutACLs, [`:5742-5744`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5742-L5744)) | `s3:x-amz-acl` (always) and the grant headers (when any present) |

Tags: `needsTags(policies...)` returns `existing` when any policy has a condition key starting with `s3:ExistingObjectTag` (only when the caller asks for it), and `resource` when any has a key starting with `s3:ResourceTag` or a value starting with `${s3:ResourceTag` (`:777-819`); `addTags` decodes `attrs[tags.Attr]` and adds `s3:ExistingObjectTag/<k>` and `s3:ResourceTag/<k>` for each tag under the two flags ([`:708-725`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L708-L725)); an undecodable tag set is an error (`-EIO`, mapped to `op.ErrInternalError` by the caller). Which attrs feed which key is Task 10's per-action table.

- [ ] **Step 1: Write the failing specs**

`identity_test.go`: with `alice := meta.UserInfo{UserID: meta.ParseUserID("t$alice"), DisplayName: "Alice", SubUsers: map[string]meta.SubUser{"ro": {Name: "ro", Perm: uint32(acl.PermRead)}}, Type: meta.IdentityRGW}` and identities built as `op.Identity{User: &alice, Owner: meta.UserOwner(alice.UserID), Tenant: "t"}`:
- `IsOwnerOf("t$alice")` true, `("alice")` false, `("t$alice2")` false; with `Account: &meta.AccountInfo{ID: "RGW12345678901234567"}`, `IsOwnerOf("RGW12345678901234567")` true.
- `IsAnonymous` of `op.Anonymous()` true, of alice false.
- `PermsFromACLSpec({"t$alice": 3})` is 3; with the account, the account id's entry is or-ed in.
- `IsIdentity`: wildcard true; `AccountPrincipal("t")` true (tenant), `AccountPrincipal("u")` false; `UserPrincipal("t", "alice")` true; `UserPrincipal("", "alice")` false (tenant mismatch); `UserPrincipal("t", "alice:ro")` true only when `SubUser == "ro"`; `UserPrincipal("t", "alice:*")` true only with a subuser; `UserPrincipal("t", "alic")` false; with `Path: "/eng/"`, `UserPrincipal("t", "eng/alice")` true and `UserPrincipal("t", "alice")` false (`:33-42`); account user with `Account.ID == "RGW…"`: `UserPrincipal("RGW…", "Alice")` matches through the display name (`:1067-1069`); `RolePrincipal` false.
- `PermMask`: no subuser FULL_CONTROL; `SubUser: "ro"` READ; `SubUser: "nope"` NONE.
- `PolicyTenant`: `"t"` for alice; nil with an account.

`env_test.go`: a request with `RemoteAddr "10.1.2.3:5555"`, `Referer`, `User-Agent`, `TLS: true`, alice, `SubUser "ro"`, clock fixed at `2026-09-27T12:00:00.5Z`: the base keys equal the table (`aws:CurrentTime` `1790510400`, `aws:EpochTime` `2026-09-27T12:00:00.500000000Z`, `aws:SourceIp` `10.1.2.3`, `aws:SecureTransport` `true`, `aws:username` `alice`, `rgw:subuser` `ro`, `sts:authentication` `false`); `RemoteAddrParam: "HTTP_X_FORWARDED_FOR"` with `X-Forwarded-For: 192.168.1.4, 4.3.2.1` gives `192.168.1.4` (`IPPolicyTest.IPEnvironment`, [`test_rgw_iam_policy.cc:1097-1141`](https://github.com/ceph/ceph/blob/v19.2.6/src/test/rgw/test_rgw_iam_policy.cc#L1097-L1141)) and no `aws:SourceIp` when the header is absent; `TLS: false` with `TrustForwardedHTTPS` and `X-Forwarded-Proto: https` gives `true`, without the option absent; `S3ListBucket` with `?prefix=a/&delimiter=/&max-keys=7` adds the three keys, with no `max-keys` adds `1000`; `S3PutObject` with no headers adds `s3:x-amz-acl` = `""` and no grant keys; with `X-Amz-Acl: public-read`, `X-Amz-Grant-Read: id="bob"`, `X-Amz-Tagging: k=v&k2=v2` adds the acl, the one grant key and two `s3:RequestObjectTag/*`; `S3PutObject` with `?uploads` adds none of those; `S3GetObject` adds only the base keys.

`load_test.go`: `bucketACL` of a record without `meta.AttrACL` is `DefaultPolicy(rec.Info.Owner, "")` (`:280-283`, empty display name); with an encoded policy it decodes; a corrupt attr is an error. `objectACL` without the attr defaults to the bucket ACL owner with its display name ([`:307-311`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L307-L311)). `bucketPolicy` returns nil for no attr, parses with the tenant, and returns the `*policy.ParseError` for a bad document. `identityPolicies` on `op.Anonymous()` is empty; with `Attrs["user.rgw.user-policy"]` holding example1 returns one policy parsed with tenant `t`; with `Attrs["user.rgw.managed-policy"]` naming `AmazonS3ReadOnlyAccess` returns it; both attrs concatenate user policies first ([`:163-168`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_managed_policy.cc#L163-L168)). `publicAccess` decodes a hand-built block and returns nil for garbage ([`:349-352`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L349-L352)). `needsTags`: a policy with `StringEquals s3:ExistingObjectTag/x` → existing; with `${s3:ResourceTag/x}` value → resource. `addTags` with two tags and both flags adds four keys.

- [ ] **Step 2: Run to see them fail**; **Step 3: Implement**; **Step 4: Run to see them pass**

Run: `go test -tags=ceph_preview ./internal/authz/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/authz
git commit -m "feat(authz): view identities as radosgw's applier does, build the IAM environment, load ACLs and policies"
```

---

### Task 10: `authz`: the `Evaluator`

**Files:**
- Create: `internal/authz/evaluator.go`, `internal/authz/evaluator_test.go`
- Modify: `docs/exclusions.md` (coexistence section: IAM group members refused, D-Z2, Step 6)

**Interfaces:**
- Consumes: Task 9; `op.Authorizer`; `memstore`; phase 0's `meta.UserInfo.GroupIDs`.
- Produces:

```go
package authz

// Evaluator implements op.Authorizer: radosgw's verify_user_permission,
// verify_bucket_permission and verify_object_permission (rgw_common.cc:1250-1637).
type Evaluator struct {
	cfg         Config
	sem         policy.Semantics
	groupWarned sync.Map // user id -> struct{}: IAM group members already logged
}
func New(cfg Config) *Evaluator
var _ op.Authorizer = (*Evaluator)(nil)

// deniedAsGroupMember is AccessDenied for an identity that belongs to an IAM
// group, logged once per user. Every exported Verify method runs it first.
func (e *Evaluator) deniedAsGroupMember(ctx context.Context, id *op.Identity) error

func (e *Evaluator) VerifyUser(ctx context.Context, r *op.Request, a policy.Action) error
func (e *Evaluator) VerifyBucket(ctx context.Context, r *op.Request, a policy.Action, perm acl.Permission) error
func (e *Evaluator) VerifyObject(ctx context.Context, r *op.Request, a policy.Action, perm acl.Permission) error

// VerifyBucketIn is VerifyBucket against an explicit bucket and the key the
// ARN carries (Name "" for the bucket ARN): CopyObject's source bucket
// (rgw_op.cc:5453-5460) and DeleteObjects' per-key check (:6826-6838). The
// public-access block and requester-pays are still r.BucketRec's, the
// request's bucket, as perm_state_from_req_state takes them from s; ACL,
// policy and ARN are bucket's and key's. Implements op.Authorizer.
func (e *Evaluator) VerifyBucketIn(ctx context.Context, r *op.Request, a policy.Action, perm acl.Permission, bucket *op.BucketRecord, key meta.ObjKey) error

// VerifyObjectIn is VerifyObject against an explicit object in an explicit
// bucket: RGWPutObj's copy source (:3920-3963) and a multipart upload's meta
// object (:398-418). Implements op.Authorizer.
func (e *Evaluator) VerifyObjectIn(ctx context.Context, r *op.Request, a policy.Action, perm acl.Permission, bucket *op.BucketRecord, obj *op.ObjectState) error
```

Every method returns `nil`, `op.ErrAccessDenied`, `op.ErrNoSuchKey` (D-Z5), `op.ErrUserSuspended` (a suspended bucket), `op.ErrPermanentRedirect` is not Z's (zonegroup mismatch is M's `Init`), or `op.ErrInternalError` wrapping a decode failure. Debug logs name the deciding rule with `slog.DebugContext(ctx, "authorization decided", slog.String("op", a.String()), slog.String("rule", ...))`, never the policy text.

**IAM group members (D-Z2).** Every exported method (`VerifyUser`, `VerifyBucket`, `VerifyObject`, `VerifyBucketIn`, `VerifyObjectIn`) starts with `e.deniedAsGroupMember(ctx, &r.Identity)` and returns its error, before the suspended-bucket check, the missing-object rule and every policy and ACL: a group's Deny could refuse any of them in radosgw. The helper, in `evaluator.go`:

```go
// deniedAsGroupMember refuses an identity that belongs to an IAM group.
// radosgw loads each group's inline and managed policies with the user's
// own at authentication (load_account_and_policies, rgw_auth.cc:170-184 at
// v19.2.6 and v20.2.4) and evaluates them on every request, so a group's
// Deny refuses what the user's policies and the ACLs allow. With no group
// store to read them from, evaluating the rest could grant what radosgw
// denies; an admin identity still passes through op.Run's override, as it
// does after any policy denial (rgw_process.cc:228-236 at v19.2.6).
func (e *Evaluator) deniedAsGroupMember(ctx context.Context, id *op.Identity) error {
	if id.User == nil || len(id.User.GroupIDs) == 0 {
		return nil
	}
	uid := id.User.UserID.String()
	if _, logged := e.groupWarned.LoadOrStore(uid, struct{}{}); !logged {
		slog.WarnContext(ctx, "refusing an iam group member: group policies are not evaluated",
			slog.String("user", uid), slog.Int("groups", len(id.User.GroupIDs)))
	}
	return fmt.Errorf("%w: %s belongs to an iam group", op.ErrAccessDenied, uid)
}
```

The set of logged users grows by one entry per distinct group member, which only the IAM API can create.

**`VerifyUser`** (`verify_user_permission`, [`:1250-1320`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1250-L1320); callers [`:2489-2500`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2489-L2500), [`:3215-3245`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3215-L3245)):

1. For `S3CreateBucket`: anonymous → `ErrAccessDenied` (`:3220-3222`); ARN is `BucketARN(r.Tenant, r.Bucket)`. Otherwise ARN is `ARN{PartitionAWS, ServiceS3, "", r.Identity.Tenant, "*"}` ([`:2494-2495`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2494-L2495)).
2. `env := BuildEnv(cfg, r, a)`; `ids, err := identityPolicies(&r.Identity, release)`; `accountRoot := IdentityType() == meta.IdentityRoot`; `mandatory := r.Identity.Account != nil` (`:1305-1308`).
3. `effect := evaluate(env, id, accountRoot, a, arn, nil /* no resource policy */, ids)`; Deny → `ErrAccessDenied`; Allow → continue to step 5.
4. Pass: `mandatory` → `ErrAccessDenied` ([`:1268-1272`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1268-L1272)); else `verify_user_permission_no_policy` ([`:1281-1297`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1281-L1297)): a role identity → `ErrAccessDenied`; S3 has no user ACL, so the owner is empty and the result is allowed (`:1289-1290`) — the perm-mask check at `:1292` is never reached for S3.
5. For `S3CreateBucket` only: `r.Identity.Tenant != r.Tenant` and the identity is not a role → `ErrAccessDenied` (`:3231-3240`). The max-buckets check (`:3242`) is M's.

**`evaluate`** is `evaluate_iam_policies` ([`:1170-1248`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1170-L1248)) without session policies (roles are phase 3; with none, [`:1195-1229`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1195-L1229) is skipped): `idRes := evalMany(ids)` (`eval_identity_or_session_policies`, [`:1146-1168`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1146-L1168): any Deny → Deny; any Allow → Allow; else Pass), Deny → Deny; `resRes := resource.Eval(env, id, a, &arn, sem)` when a resource policy is given, else Pass; Deny → Deny; `resRes == Allow` → Allow; `idRes == Allow` → Allow; `accountRoot` → Allow; else Pass. Identity policies are evaluated with a nil identity (`boost::none`, [`:1153`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1153)), so their `Principal` elements are ignored, exactly as radosgw.

**`VerifyBucket`** (`verify_bucket_permission(req_state, arn, op)`, [`:1374-1410`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1374-L1410), [`:1461-1477`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1461-L1477)) is `VerifyBucketIn(ctx, r, a, perm, r.BucketRec, r.Object)`; `r.BucketRec == nil` → `ErrAccessDenied` ([`:1471-1474`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1471-L1474)). **`VerifyBucketIn`**:

1. `rec := bucket`; suspended: `rec.Info.Flags&meta.BucketSuspended != 0` and not bypassed (Squid: `!r.Identity.System`; Tentacle (`sem.AdminBypass`): `!r.Identity.Admin`) → `op.ErrUserSuspended` (`read_bucket_policy`, [`rgw_op.cc:365-369`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L365-L369)).
2. `bacl := bucketACL(rec)`; `bucketOwner := bacl.Owner` (`s->bucket_owner`, [`:552`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L552)); `bp, err := bucketPolicy(rec, r.Tenant, release)`: a `*policy.ParseError` is `ErrAccessDenied` unless bypassed (Squid `System`, Tentacle `Admin`; [`:598-613`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L598-L613)), so that an admin can repair the bucket; `pab := publicAccess(r.BucketRec)` (always the request's bucket, `perm_state_from_req_state`, [`rgw_common.cc:1088`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1088)); `ids := identityPolicies(...)`.
3. `arn := BucketARN(rec.EntryPoint tenant, name)` when `key.Name == ""`, else `ObjectARN(..., key.Name)`.
4. `env := BuildEnv(cfg, r, a)`; tags: `existing, resource := needsTags(...)` over `bp` and `ids` with `existing` only for object-keyed calls; bucket-scope actions add the bucket's `tags.Attr` under `resource` (`rgw_iam_add_buckettags`); object-keyed bucket calls (PutObject, DeleteObject, InitMultipart, CompleteMultipart, AbortMultipart, DeleteObjects per key) add `r.ObjState.Attrs` tags under both flags when the object is loaded (`rgw_iam_add_objtags`, [`:5140-5142`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5140-L5142), [`:6273-6275`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6273-L6275), [`:6347-6349`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6347-L6349), [`:6597-6599`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6597-L6599), [`:6770-6772`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6770-L6772)); PutObject additionally adds bucket tags under `resource` ([`:3978-3980`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3978-L3980)); CopyObject's destination check adds the destination bucket tags ([`:5477-5480`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5477-L5480)).
5. Tentacle only: `sem.ExpectedBucketOwner` and header `X-Amz-Expected-Bucket-Owner` present and not equal to `to_expected_bucket_owner(rec.Info.Owner)` (a user's `ID` without tenant, or the account id; [`[T]:232-240`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L232-L240)) → `ErrAccessDenied`.
6. Requester pays (`verify_requester_payer_permission`, [`:1322-1340`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1322-L1340), on `r.BucketRec.Info.RequesterPays`): owner → ok; anonymous → `ErrAccessDenied`; header `X-Amz-Request-Payer` or query `x-amz-request-payer` equal to `requester` case-insensitively → ok; present with another value → `ErrAccessDenied` (`nullopt` → false); absent → `ErrAccessDenied`.
7. Tentacle only: `sem.RestrictPublicBuckets && pab != nil && pab.RestrictPublicBuckets && bp != nil && bp.IsPublic(sem) && !IsOwnerOfOwner(r.BucketRec.Info.Owner)` → `ErrAccessDenied` ([`[T]:1374-1380`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1374-L1380)).
8. Account identity (`:1374-1400`): `accountRoot := IdentityType() == meta.IdentityRoot`; cross-account (`!IsOwnerOf(bucketOwner.ID)`): `evaluate(env, id, accountRoot, a, arn, nil, ids)` must be Allow AND the resource side `evaluate(env, id, false, a, arn, bp, nil)` must be Allow or, when Pass, `noPolicyBucket(...)` with the ACLs must grant; same-account: `evaluate(env, id, accountRoot, a, arn, bp, ids)` must be Allow (Pass is denied: "don't consult acls for same-account access").
9. Non-account identity: `effect := evaluate(env, id, false, a, arn, bp, ids)`; Deny → `ErrAccessDenied`; Allow → nil; Pass → `noPolicyBucket(id, bacl, perm, referer, pab)`.
10. `noPolicyBucket` is `verify_bucket_permission_no_policy` ([`:1412-1432`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1412-L1432)): `perm & PermMask() != perm` → deny; `bacl.Verify(id, perm, perm, r.Referer, pab != nil && pab.IgnorePublicACLs)` → allow; the user ACL is Swift-only and empty for S3, so its check never grants; deny.

**`VerifyObject`** is `VerifyObjectIn(ctx, r, a, perm, r.BucketRec, r.ObjState)` with the missing-object rule (D-Z5) first: `obj == nil || !obj.Exists` → `r.Identity.Admin` → `op.ErrNoSuchKey` ([`rgw_op.cc:434-436`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L434-L436)); else evaluate `S3ListBucket` on the bucket with `env` extended by `s3:prefix` = `obj.Key.Name` ([`:438`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L438)) through the bucket path above with `perm = acl.PermFor(S3ListBucket)`; allowed → `op.ErrNoSuchKey`, else `op.ErrAccessDenied` ([`:440-445`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L440-L445)). **`VerifyObjectIn`** for an existing object (`verify_object_permission(req_state)`, [`:1518-1558`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1518-L1558), [`:1625-1637`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1625-L1637)):

1. Suspended bucket, `bacl`, `bucketOwner`, `bp` (tenant `r.Tenant` even for a source bucket, `read_obj_policy` `:419`), `pab`, `ids` as above, with `rec := bucket`; `oacl := objectACL(obj, bucketOwner)`.
2. `arn := ObjectARN(tenant, name, obj.Key.Name)` (`ARN(rgw_obj)` carries the key name, not the instance).
3. `env` plus object tags from `obj.Attrs` under `needsTags` with `existing` allowed (`:985-987`); PutACLs on an object adds them under both flags unconditionally ([`:5747`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5747)).
4. Expected owner (Tentacle) and requester pays as for buckets.
5. Tentacle `RestrictPublicBuckets` as for buckets ([`[T]:1541-1547`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1541-L1547)).
6. Account identity (`:1531-1554`): `objectOwner := oacl.Owner.ID` when non-empty else `bucketOwner.ID`; cross-account when `!IsOwnerOf(objectOwner)`: identity side Allow AND resource side (`bacl`, `oacl`, `bp`, no identity policies) Allow-or-ACL; same-account: policies only.
7. Non-account: `evaluate(env, id, false, a, arn, bp, ids)`; Deny → `ErrAccessDenied`; Allow → nil; Pass → `noPolicyObject`.
8. `noPolicyObject` is `verify_object_permission_no_policy` ([`:1560-1608`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1560-L1608)): `defer_to_bucket_acls` is Swift-only and zero → skip [`:1567-1571`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1567-L1571); `oacl.Verify(id, PermMask(), perm, "" /* nullptr */, ignorePublicACLs)` → allow ([`:1573-1580`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1573-L1580)); `!cfg.EnforceSwiftACLs` → deny; `perm & PermMask() != perm` → deny; `swiftPerm := 0`; `perm & (READ|READ_ACP) != 0` → `|= PermReadObjs`; `perm & WRITE != 0` → `|= PermWriteObjs`; zero → deny; `bacl.Verify(id, swiftPerm, swiftPerm, r.Referer, false)` → allow ([`:1601-1604`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1601-L1604)); the user ACL never grants for S3; deny. This fallback matters only for buckets a Swift client wrote `.r:` or `READ_OBJS` grants into; it is transcribed because those bits are stored data on a shared zone.

- [ ] **Step 1: Write the failing specs**

`evaluator_test.go` seeds a `memstore.Store` with users `alice` (tenant `""`), `bob`, `carol` (tenant `t2`), a subuser `alice:ro` with `PermRead`, and `acct` (an account user with `AccountID` `RGW00000000000000001` and `Account` info) and `root` (the same account, `Type: meta.IdentityRoot`); buckets `pub` owned by alice with a `public-read` ACL attr, `priv` owned by alice with the default ACL, `payer` with `RequesterPays`, `susp` with `BucketSuspended`, `blocked` with a public-access attr `{IgnorePublicACLs: true}` and a `public-read` ACL, `polbkt` with a bucket policy attr; objects `priv/o` (alice, default ACL), `priv/shared` (an object ACL granting bob READ), `priv/tagged` (tags `{env: prod}`). A helper `req(id op.Identity, bucket, key string, hdr ...string) *op.Request` runs `Init`-equivalent loading through the store (`GetBucket`, `StatObject`) so `BucketRec` and `ObjState` are filled, sets `Env` with `Zone: store`, and returns it. `eval := authz.New(authz.Config{Release: denc.Squid, EnforceSwiftACLs: true, RejectInvalidPrincipals: true, RemoteAddrParam: "REMOTE_ADDR", ACLGrantsMaxNum: 100})`, and a second one for `denc.Tentacle`.

Specs, each an `It` with got/want in the message, `MatchError(op.ErrAccessDenied)` etc.:

- **ACL only.** alice GET `priv/o` ok; bob GET `priv/o` denied; anonymous GET `pub` list ok (`S3ListBucket`, READ); anonymous PUT `pub/x` denied; bob GET `priv/shared` ok (object ACL grants READ), bob PUT ACL on `priv/shared` denied (WRITE_ACP not granted); alice GET ACL of `priv/o` ok (owner gets READ_ACP); `blocked`: anonymous list denied (Review Focus 4), alice ok.
- **Subuser.** `alice:ro` GET `priv/o` ok; PUT `priv/x` denied by `PermMask` though the ACL grants FULL_CONTROL (Review Focus 5); `alice:ro` ListBuckets ok (`VerifyUser` never checks the mask for S3, `:1289-1290`).
- **Bucket policy.** `polbkt` policy `Allow bob s3:GetObject arn:aws:s3:::polbkt/*`: bob GET ok; bob LIST denied (no ACL, no policy); carol (tenant `t2`) GET denied; add a `Deny * s3:GetObject polbkt/secret*` statement: alice GET `polbkt/secret1` denied although alice owns the bucket (Deny beats ACL FULL_CONTROL, [`:1187-1190`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1187-L1190)); alice PUT ok; bob GET `polbkt/secret1` denied; a policy with `Principal: "*"` and `IpAddress aws:SourceIp 10.0.0.0/8`: anonymous GET from `10.1.1.1:1` ok, from `192.168.1.1:1` denied.
- **Stored identity policies (D6, Review Focus 1).** bob with `Attrs["user.rgw.user-policy"]` = `{"deny": Deny s3:GetObject arn:aws:s3:::priv/*}`: bob GET `priv/shared` denied even though the object ACL grants READ and even with a bucket policy allowing bob; alice with the same Deny denied on her own bucket; bob with `Allow s3:ListBucket arn:aws:s3:::priv` and no ACL grant: LIST ok (identity Allow suffices for a non-account user, [`:1237-1240`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1237-L1240)); bob with `user.rgw.managed-policy` naming `AmazonS3ReadOnlyAccess`: GET `priv/o` ok, PUT denied by ACL fallback; a corrupt user-policy attr → `ErrInternalError`... no: `load_inline_policy` throws and `rgw_process_authenticated` fails the request with 500? `PolicyParseException` propagates from `load_account_and_policies`, caught in the auth strategy as a generic failure → `-EACCES`? Read [`rgw_auth.cc:489-556`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L489-L556) (`Strategy::apply`) while implementing and pin the observed mapping in the spec: it catches `std::exception` and returns `-EPERM` (AccessDenied). Assert `ErrAccessDenied`.
- **Account users.** `acct` GET on `priv/o` (alice's, cross-account) with no policies → denied (identity side must Allow, `:1381-1389`); with an identity policy Allow and an object ACL granting the account id READ → ok; with the identity Allow but no ACL and no bucket policy → denied (resource side needs Allow or ACL); `root` GET on a bucket owned by the account with no policies → ok (`account_root`, [`:1242-1245`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1242-L1245)); `acct` GET on its own account's bucket with no policies → denied (same-account ignores ACLs, [`:1391-1397`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1391-L1397)); `acct` ListBuckets with no policies → denied (`mandatory`, `:1305-1308`), with an identity Allow → ok.
- **Tenants.** carol PUT bucket `t2:cb` (`r.Tenant == "t2"`) ok; alice CreateBucket with `r.Tenant = "t2"` → denied ([`:3231-3240`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3231-L3240)); bob GET `t2:cb/o` with a bucket policy on `cb` naming `arn:aws:iam:::user/bob` → denied (principal account `""` ≠ tenant `t2`... wait: the principal's account is the user's tenant; bob's tenant is `""`, so `UserPrincipal("", "bob")` matches bob (`p.Account == User.UserID.Tenant`); the resource `arn:aws:s3:::cb/*` was rewritten to tenant `t2` at parse; the request ARN is `arn:aws:s3::t2:cb/o` → matches) → **ok**; and with `Principal: arn:aws:iam::t2:user/bob` → denied (bob is not in tenant t2).
- **Missing object (Review Focus 2).** bob GET `priv/nope` → `ErrAccessDenied`; alice GET `priv/nope` → `ErrNoSuchKey`; anonymous GET `pub/nope` → `ErrNoSuchKey` (AllUsers READ grants ListBucket); admin identity GET `priv/nope` → `ErrNoSuchKey`; a bucket policy `Allow bob s3:ListBucket arn:aws:s3:::priv` with `Condition StringLike s3:prefix "public/*"`: bob GET `priv/public/x` (missing) → `ErrNoSuchKey`, bob GET `priv/private/x` → `ErrAccessDenied`.
- **Requester pays.** bob on `payer` with a bucket policy allowing him: no header → denied; `X-Amz-Request-Payer: requester` → ok; `X-Amz-Request-Payer: other` → denied; alice (owner) without header ok; anonymous denied even with AllUsers READ.
- **Suspended.** alice GET `susp/o` → `ErrUserSuspended`; a system identity → not suspended-denied on Squid; an admin (non-system) identity → `ErrUserSuspended` on Squid and not on Tentacle (`sem.AdminBypass`).
- **Unparsable stored bucket policy.** a bucket whose `user.rgw.iam-policy` attr is `{not json` → alice denied on Squid; the system identity passes ([`:611-613`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L611-L613)); on Tentacle the admin passes.
- **Tags.** bucket policy on `priv`: `Allow bob s3:GetObject priv/* Condition StringEquals s3:ExistingObjectTag/env prod`: bob GET `priv/tagged` ok, `priv/o` denied; with `s3:ResourceTag/team` on the bucket's tags: bob LIST ok only when the bucket carries the tag; `s3:RequestObjectTag/env` on PUT with `X-Amz-Tagging: env=prod` ok, without denied.
- **Explicit inputs.** `VerifyObjectIn(…, srcBucket, srcObj)` for a copy: bob denied on alice's private source, ok on a source object whose ACL grants bob READ; `VerifyBucketIn(…, priv, key "a")` for DeleteObjects: alice ok, bob denied; `VerifyBucketIn` with `key.Name == ""` uses the bucket ARN (a policy on `arn:aws:s3:::priv` matches, one on `priv/*` does not); the request's bucket, not the explicit one, supplies the public-access block and requester-pays: with `r.BucketRec` a requester-pays bucket bob does not own and no `x-amz-request-payer`, `VerifyBucketIn(…, pub, key)` on a public source is denied, and with `r.BucketRec` bob's own bucket and the SOURCE requester-pays it is allowed (`RGWCopyObj::verify_permission`, [rgw_op.cc:5454-5457](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5454-L5457) at v19.2.6, `s` is the destination).
- **Tentacle rows.** `X-Amz-Expected-Bucket-Owner: bob` on alice's bucket → denied on Tentacle, ignored on Squid; `RestrictPublicBuckets` with a public policy: bob denied on Tentacle, allowed on Squid.
- **Anonymous ListBuckets / CreateBucket.** anonymous `VerifyUser(S3ListAllMyBuckets)` ok (no policy, no user ACL; the op itself renders the anonymous result as G does); anonymous `VerifyUser(S3CreateBucket)` denied.
- **IAM group members (D-Z2, Review Focus 6).** A fixture user `gina` with `GroupIDs: []string{"g1"}` and no stored policy owns bucket `gb` (default ACL) and object `gb/o`. On both releases: `VerifyUser(S3ListAllMyBuckets)`, `VerifyBucket` listing her own `gb`, `VerifyObject` of her own `gb/o`, `VerifyObject` of `pub/x` (public-read), `VerifyBucketIn` and `VerifyObjectIn` on the same records are each `op.ErrAccessDenied`, although the owner's FULL_CONTROL or the public grant would allow them; `VerifyObject` of a missing `gb/nope` is `op.ErrAccessDenied`, not `op.ErrNoSuchKey`; `gina` with a stored user policy allowing `s3:*` is still denied. With slog captured to a buffer (a JSON handler swapped in as the default), five denials of `gina` (tenant `""`) produce exactly one warning with `"user":"gina"`, and a denial of a second member `hugo` adds a second warning. A user with empty `GroupIDs` is unaffected (the specs above).

- [ ] **Step 2: Run to see them fail**; **Step 3: Implement**; **Step 4: Run to see them pass**

Run: `go test -tags=ceph_preview -race ./internal/authz/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git commit -am "feat(authz): implement op.Authorizer with radosgw's ACL, bucket-policy and identity-policy precedence"
```

- [ ] **Step 6: Record the difference in `docs/exclusions.md`; commit**

Refusing IAM group members is a difference from radosgw this task introduces (D-Z2), so the entry lands in its PR. In `docs/exclusions.md`, "Coexistence obligations independent of any exclusion", append:

```markdown
- **Members of IAM groups are refused until phase 3 evaluates group
  policies.** radosgw loads, with a user's own identity policies, the
  inline and managed policies of every IAM group the user's record lists
  (`load_account_and_policies`, `rgw_auth.cc:170-184` at v19.2.6 and
  v20.2.4) and evaluates them on every request. rgw-go phase 1 cannot read
  groups, which only the IAM API creates and rgw-go serves from phase 3, so
  it answers every request of a user whose record lists a group with 403
  AccessDenied rather than evaluate the request without the group's
  policies, which would allow what a group's Deny forbids. An admin or
  system user still passes, as radosgw's own policy denials let one. Account
  users in IAM groups therefore work through radosgw and not through rgw-go
  until phase 3.
```

```sh
git add docs/exclusions.md
git commit -m "docs(exclusions): record that IAM group members are refused until group policies are evaluated"
```

---

### Task 11: `authz`: write-side helpers, `ErrorFor`, `cli serve` wiring, end-to-end

**Files:**
- Create: `internal/authz/write.go`, `internal/authz/errors.go`, `internal/authz/write_test.go`, `internal/authz/errors_test.go`, `internal/authz/e2e_test.go`
- Modify: `internal/cli/serve.go` (step 7 of G's Task 9: `env.Authz = authz.New(cfg)`), `internal/cli/root_test.go` if the wiring is observable there

**Interfaces:**
- Consumes: Tasks 7, 10; `op.UserStore`; `s3.NewHandler`, `s3.WriteError`; `op.ListBuckets`.
- Produces:

```go
package authz

// UserResolver implements acl.Resolver over op.UserStore. Accounts is
// op.AccountStore in every caller (it has AccountName, so the literal is
// authz.UserResolver{Users: r.Env.Users, Accounts: r.Env.Accounts});
// when nil, an account-id owner or grantee cannot be resolved and is reported
// as acl.ErrNoSuchOwner, which radosgw would also report for an unknown id.
// While the driver's account lookups are not implemented, its AccountName
// answers op.ErrNotImplemented, which Resolve reports the same way.
type UserResolver struct {
	Users    op.UserStore
	Accounts AccountLookup // op.AccountStore satisfies it; may be nil in tests
}
type AccountLookup interface {
	AccountName(ctx context.Context, id string) (string, error)
}

// ErrorFor maps the sentinels of acl, policy and tags to op errors with
// radosgw's messages: acl.ErrUnresolvableEmail → op.ErrUnresolvableGrantByEmail;
// *acl.ParseError and acl.ErrInvalid, acl.ErrNoSuchOwner → op.ErrInvalidArgument
// with the message; acl.ErrOwnerMismatch → op.ErrAccessDenied "Cannot modify ACL Owner";
// acl.ErrTooManyGrants → op.ErrLimitExceeded with the message; *policy.ParseError →
// op.ErrInvalidArgument with e.Error(); tags.ErrInvalidTag → op.ErrInvalidTag;
// tags.ErrMalformedXML → op.ErrMalformedXML; anything else unchanged.
func ErrorFor(err error) error

// BuildACL is RGWPutACLs::execute's derivation and validation of the new
// policy (rgw_op.cc:5821-5895, :5905-5910) for a bucket (object false) or an
// object: a canned ACL with a body is ErrInvalid; a canned ACL or grant
// headers build through Canned/FromHeaders with the existing owner (a
// bucket ignores the bucket-* canned names, rgw_rest_s3.cc:3637-3647);
// otherwise the body parses through ParseS3XML; the owner may not change;
// more than cfg.ACLGrantsMaxNum grants is ErrTooManyGrants; a public ACL on
// a bucket whose public-access block sets BlockPublicACLs is op.ErrAccessDenied.
func (e *Evaluator) BuildACL(ctx context.Context, r *op.Request, res acl.Resolver, existing acl.Policy, body []byte, object bool) (acl.Policy, error)

// BuildDefaultACL is create_s3_policy (rgw_rest_s3.cc:2408-2422) for
// CreateBucket, PutObject, InitMultipart and CopyObject: grant headers with a
// canned ACL is op.ErrInvalidRequest; grant headers alone build through
// FromHeaders; otherwise Canned with the X-Amz-Acl value (empty is private).
func (e *Evaluator) BuildDefaultACL(ctx context.Context, r *op.Request, res acl.Resolver, owner, bucketOwner acl.Owner) (acl.Policy, error)

// ParseBucketPolicy is RGWPutBucketPolicy::execute's parse (rgw_op.cc:8098-8118):
// tenant r.Tenant, RejectInvalidPrincipals from cfg, the cluster's release; a
// public policy on a bucket whose block sets BlockPublicPolicy is
// op.ErrAccessDenied. The stored attr is p.Text.
func (e *Evaluator) ParseBucketPolicy(r *op.Request, body []byte) (*policy.Policy, error)
```

`acl` gains `ErrOwnerMismatch` and `ErrTooManyGrants` sentinels in this task (they are `PutACLs`' errors but belong beside the others).

`cli/serve.go`: `cfg, err := authz.ConfigFrom(conf, store.Release()); env.Authz = authz.New(cfg)` in place of `op.OwnerOnly{}`; `op.OwnerOnly` stays in `op` for G's specs.

- [ ] **Step 1: Write the failing specs**

`write_test.go`: `BuildACL` with `X-Amz-Acl: public-read` and an empty body on a bucket gives the canned policy with the existing owner; with `X-Amz-Acl: bucket-owner-read` on a bucket gives `private` (ignored name); the same on an object honours it; a canned header with a non-empty body → `acl.ErrInvalid` (mapped to `InvalidArgument`); a body whose owner is bob on alice's bucket → `acl.ErrOwnerMismatch`; 101 grants → `acl.ErrTooManyGrants`; a public ACL on a bucket with `BlockPublicACLs` → `op.ErrAccessDenied`; grant headers alone → `FromHeaders`. `BuildDefaultACL`: no headers → private for the owner; `X-Amz-Grant-Read` plus `X-Amz-Acl` → `op.ErrInvalidRequest`; an anonymous owner uses the bucket owner. `ParseBucketPolicy`: a valid document returns `Text` equal to the body; `s3:GetObjectAttributes` fails on Squid, parses on Tentacle; `Resource` in another tenant fails; a public policy with `BlockPublicPolicy` → `op.ErrAccessDenied`; `Principal: {"CanonicalUser": "x"}` fails (RejectInvalidPrincipals true by default). `errors_test.go`: each mapping in the table, including that `op.Err*` values pass through and a plain error is unchanged.

`e2e_test.go`: `s3.NewHandler(env, auth, cfg)` with `env.Authz = authz.New(...)`, a stub `s3.AuthenticatorFunc` (G Task 4's three-argument form, ignoring the payload forms) returning bob's identity with a stored `user.rgw.user-policy` denying `s3:ListAllMyBuckets`: `GET /` answers 403 `AccessDenied` in radosgw's error document; with the Deny removed, 200 (G's `op.ListBuckets` calls `VerifyUserPermission`; this proves `Run` → `Authorizer` → `evaluate` end to end through the only op G ships). A second case: an admin identity with the same Deny → 200 (G's `Run` override). A third, for D-Z2: bob with `User.GroupIDs = []string{"g1"}` and no stored policy → 403 `AccessDenied`; the same identity with `Admin: true` → 200, the override applying to the group refusal as to any policy denial.

- [ ] **Step 2: Run to see them fail**; **Step 3: Implement**; **Step 4: Run to see them pass**

Run: `make check`
Expected: green, including `generate-check` (no new fakes) and `lint`.

- [ ] **Step 5: Commit**

```sh
git commit -am "feat(authz): build ACLs and bucket policies for the write ops and serve with the real authorizer"
```

Open the draft PR `feat(authz): the real authorizer (unit Z tasks 9-11)`. The PR description states that members of IAM groups are refused until phase 3 evaluates group policies (D-Z2, with its `docs/exclusions.md` entry from Task 10) and that session policies and roles are phase 3.

---

### Task 12: Gate: s3-tests ACL, policy and tagging groups; upstream-bug entries `[cluster]`

**Files:**
- Create: `test/s3tests/z-groups.txt` (the s3-tests node-id patterns Z is judged by, P Task 11's form: the names listed under Step 1, one regular expression per line, checked against `test/s3tests/baseline/squid.json` so every baseline name matching a pattern is Z's)
- Modify: `docs/ceph-upstream-bugs.md` (three entries), `docs/cgo-limitations.md` only if a task above found one (none expected)

**Interfaces:**
- Consumes: T Task 12's `make s3tests-parity RELEASE=<r>` and T Task 5's `go run ./hack/parity diff -baseline FILE -candidate FILE` (there is no `compare` subcommand and no `-k`; `run.sh` writes one file, `hack/s3tests/out/<release>-rgw-go-parity.xml`, which the target records to `hack/rooket/out/<release>/s3tests-rgw-go.json`), the radosgw baselines `test/s3tests/baseline/{squid,tentacle}.json`; units M, R and W merged (the ops that store ACLs, policies and tags and read objects).

- [ ] **Step 1: Run the parity comparison on Squid**

```sh
make cluster-up RELEASE=squid && make populate RELEASE=squid && make rgw-go-up RELEASE=squid
make s3tests-parity RELEASE=squid
go run ./hack/parity diff -baseline test/s3tests/baseline/squid.json -candidate hack/rooket/out/squid/s3tests-rgw-go.json | grep -E -f test/s3tests/z-groups.txt
```

`make s3tests-parity` exits 1 while other units' differences remain; the grep is Z's verdict. Expected: the grep prints nothing.

The groups that must match radosgw's outcome exactly: every test whose name contains `acl` (`test_bucket_acl_*`, `test_object_acl_*`, `test_bucket_acl_grant_*`, `test_object_acl_full_control_verify_*`, `test_bucket_acl_no_grants`, `test_bucket_header_acl_grants`, `test_object_header_acl_grants`), `test_access_bucket_*` (anonymous and public access), every test marked `bucket_policy` (`test_bucket_policy*`, `test_bucketv2_policy*`, `test_bucket_policy_put_obj_*`, `test_bucket_policy_get_obj_*`, `test_bucket_policy_*_tagging`), the `tagging` marker (`test_put_obj_with_tags`, `test_get_obj_tagging`, `test_put_max_tags`, `test_put_excess_tags`, `test_put_max_kvsize_tags`, `test_put_excess_key_tags`, `test_put_excess_val_tags`, `test_put_modify_tags`, `test_put_delete_tags`, `test_set_bucket_tagging`) and the tenant tests. T-b's baseline is the authority for the exact names at the pinned s3-tests commit. Nineteen tests these groups would take in never run, because they call a phase 2 feature and T's phase 2 deselect list (`hack/s3tests/deselect-phase2.txt`) removes them on both gateways: the versioned-ACL tests (`test_object_put_acl_mtime`, `test_versioned_object_acl*`), the public access block and policy status tests with `acl` in their names, the CORS and presigned-OPTIONS tests with `acl` or `tenant` in theirs, and the two POST-object tests the `tagging` marker carries (`test_post_object_tags_*`).

- [ ] **Step 2: Same on Tentacle**

```sh
make cluster-up RELEASE=tentacle && make populate RELEASE=tentacle && make rgw-go-up RELEASE=tentacle
make s3tests-parity RELEASE=tentacle
go run ./hack/parity diff -baseline test/s3tests/baseline/tentacle.json -candidate hack/rooket/out/tentacle/s3tests-rgw-go.json | grep -E -f test/s3tests/z-groups.txt
```

Any mismatch is a defect in this unit; fix and rerun. A test that turns out to exercise an op outside phase 1 is not Z's to excuse: it belongs on T's deselect list for its phase (T Task 4), where it stops running on both gateways and the baselines are re-recorded. Record the pass-and-fail set the comparison printed in the PR description.

- [ ] **Step 3: Record the upstream defects**

Add to `docs/ceph-upstream-bugs.md`, in its format, with evidence at `v19.2.6` and `v20.2.4`:

1. `rgw_acl_s3.cc` `to_xml` writes owner and grantee ids and display names without XML escaping ([`:172-180`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl_s3.cc#L172-L180), [`:246-280`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl_s3.cc#L246-L280), both releases): a display name containing `&` or `<` yields an invalid GetBucketAcl document. rgw-go escapes (D-Z7); parity is unaffected because s3-tests uses plain names. Upstream status: check the tracker for an existing report before filing.
2. `rgw_build_iam_environment` sets `aws:CurrentTime` to the decimal epoch and `aws:EpochTime` to the ISO-8601 string ([`rgw_op.cc:852-853`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L852-L853), both releases), the reverse of AWS's definitions. rgw-go mirrors it.
3. Squid-only: `StringNotEquals`, `StringNotEqualsIgnoreCase`, `StringNotLike`, `NumericNotEquals`, `DateNotEquals`, `ArnNotEquals`/`ArnNotLike` evaluate as "some (env value, policy value) pair differs" ([`rgw_iam_policy.cc:894-897`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L894-L897) etc.), so a multi-valued key or a multi-valued condition is denied less than AWS would; `Null` ignores its values ([`:857-859`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L857-L859)); `is_public` counts any Allow statement without a wildcard `NotPrincipal` ([`:1900-1917`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L1900-L1917)). Fixed in Tentacle ([`[T]:876-879`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L876-L879), [`:911-1005`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L911-L1005), and the `IsPublicStatement` change). rgw-go follows the release (D-Z1).

- [ ] **Step 4: Commit**

```sh
git commit -am "docs(ceph): record the ACL XML escaping, the swapped time keys and Squid's condition semantics"
```

Report the three entries in chat so the rgw-rs session can be told. Open the draft PR `docs(ceph): authorization defects found by unit Z`; its description carries the parity sets from Steps 1 and 2.

---

## Frozen interface contract additions (what M, R, W, P and N build against)

G's contract is unchanged. Z adds the following, normative for the wave-2 plans:

### `internal/policy`

```go
// Actions: the s3 block, then the iam, sts, sns and organizations blocks.
// Every constant's String() is the radosgw name; ParseAction accepts every
// String() and "<service>:*".
func Known(a Action, r denc.Release) bool
type ActionSet [4]uint64
type ARN struct{ Partition Partition; Service Service; Region, Account, Resource string }
func BucketARN(tenant, bucket string) ARN
func ObjectARN(tenant, bucket, key string) ARN
func ParseARN(s string, wildcards bool) (ARN, bool)
type Principal struct{ Kind PrincipalKind; Account, ID, IDPURL string }
type Env map[string][]string
type Effect uint8 // Allow, Deny, Pass
type Semantics struct{ /* the Squid/Tentacle condition-operator switches */ }
func SemanticsFor(r denc.Release) Semantics
type Identity interface{ IsIdentity(Principal) bool; IdentityType() meta.IdentityType }
type Policy struct{ Text string; Version Version; ID *string; Statements []Statement }
func Parse(text string, opts ParseOptions) (*Policy, error)
type ParseOptions struct{ Tenant *string; RejectInvalidPrincipals bool; Release denc.Release }
type ParseError struct{ Offset int64; Annotation string }
func (p *Policy) Eval(env Env, id Identity, a Action, res *ARN, sem Semantics) Effect
func (p *Policy) IsPublic(sem Semantics) bool
func DecodeUserPolicies(b []byte, tenant *string, r denc.Release) ([]*Policy, error)
func DecodeManagedPolicies(b []byte, r denc.Release) ([]*Policy, error)
func ManagedPolicy(arn string, r denc.Release) (*Policy, bool)
```

### `internal/acl`

```go
const ( PermReadObjs Permission = 0x10; PermWriteObjs Permission = 0x20; PermInvalid Permission = 0xFF00 )
func PermFor(a policy.Action) Permission // op_to_perm (rgw_iam_policy.h:237-318): pass acl.PermFor(action) as the perm of op.VerifyBucketPermission / VerifyObjectPermission
type Identity interface{ IsOwnerOf(ownerID string) bool; PermsFromACLSpec(map[string]int32) Permission; IsAnonymous() bool }
func (p Policy) Perm(id Identity, mask Permission, referer string, ignorePublicACLs bool) Permission
func (p Policy) Verify(id Identity, userPermMask, perm Permission, referer string, ignorePublicACLs bool) bool
func (p Policy) IsPublic() bool
func DefaultPolicy(owner meta.Owner, displayName string) Policy
func (l *List) AddGrant(g Grant)
func (l *List) RemoveCanonUserGrant(ownerID string)
type Resolver interface{ OwnerByEmail(ctx, email string) (Owner, error); DisplayName(ctx, owner meta.Owner) (string, error) }
var ErrInvalid, ErrUnresolvableEmail, ErrNoSuchOwner, ErrOwnerMismatch, ErrTooManyGrants error
type ParseError struct{ Message string; Err error }
func ParseS3XML(ctx, r Resolver, doc []byte) (Policy, error)
func (p Policy) MarshalS3XML() []byte
func Canned(owner, bucketOwner Owner, canned string) (Policy, error)
type GrantHeaders struct{ Read, Write, ReadACP, WriteACP, FullControl string }
func FromHeaders(ctx, r Resolver, owner Owner, h GrantHeaders) (Policy, error)
const ( URIAllUsers = "http://acs.amazonaws.com/groups/global/AllUsers"; URIAuthenticatedUsers = "http://acs.amazonaws.com/groups/global/AuthenticatedUsers" )
type PublicAccessBlock struct{ BlockPublicACLs, IgnorePublicACLs, BlockPublicPolicy, RestrictPublicBuckets bool }
func DecodePublicAccessBlock(d *denc.Decoder) PublicAccessBlock
func (b PublicAccessBlock) Encode(e *denc.Encoder, _ denc.Release)
```

### `internal/tags`

```go
const Attr = "user.rgw.x-amz-tagging"
const ( MaxObjectTags = 10; MaxBucketTags = 50; MaxKeyLen = 128; MaxValueLen = 256 )
type Tag struct{ Key, Value string }
type Set struct{ Tags []Tag }
var ErrInvalidTag, ErrMalformedXML error
func (s *Set) Add(key, value string, max int) error
func (s Set) Encode(e *denc.Encoder, _ denc.Release)
func Decode(d *denc.Decoder) Set
func ParseHeader(h string, max int) (Set, error)
func ParseXML(doc []byte, max int) (Set, error)
func (s Set) MarshalXML() []byte
```

### `internal/authz`

```go
type Config struct{ Release denc.Release; RejectInvalidPrincipals, EnforceSwiftACLs bool; RemoteAddrParam string; TrustForwardedHTTPS bool; ACLGrantsMaxNum int; Now func() time.Time }
func ConfigFrom(o *cephconf.Options, r denc.Release) (Config, error)
type Evaluator struct{ /* ... */ } // implements op.Authorizer
func New(cfg Config) *Evaluator
// The two explicit-resource methods of op.Authorizer:
func (e *Evaluator) VerifyBucketIn(ctx, r *op.Request, a policy.Action, perm acl.Permission, bucket *op.BucketRecord, key meta.ObjKey) error
func (e *Evaluator) VerifyObjectIn(ctx, r *op.Request, a policy.Action, perm acl.Permission, bucket *op.BucketRecord, obj *op.ObjectState) error
func (e *Evaluator) BuildACL(ctx, r *op.Request, res acl.Resolver, existing acl.Policy, body []byte, object bool) (acl.Policy, error)
func (e *Evaluator) BuildDefaultACL(ctx, r *op.Request, res acl.Resolver, owner, bucketOwner acl.Owner) (acl.Policy, error)
func (e *Evaluator) ParseBucketPolicy(r *op.Request, body []byte) (*policy.Policy, error)
type UserResolver struct{ Users op.UserStore; Accounts AccountLookup }
func ErrorFor(err error) error
```

Rules for the callers, each from the radosgw site it mirrors:

- Which verify to call per op (the `only_bucket` table of [`rgw_rest.cc:1893-1932`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1893-L1932) and each `verify_permission`): `VerifyUserPermission` for ListBuckets (`S3ListAllMyBuckets`) and CreateBucket (`S3CreateBucket`); `VerifyBucketPermission` for every bucket-scope op, and for PutObject, DeleteObject (`S3DeleteObject`/`S3DeleteObjectVersion` by `versionId`), InitMultipart, UploadPart (`S3PutObject`), CompleteMultipart (`S3PutObject`), AbortMultipart (`S3AbortMultipartUpload`), DeleteObjects (`VerifyBucketIn` per key, plus `S3BypassGovernanceRetention` on the bucket when the header is set — phase 2 keeps that), CopyObject's destination (`S3PutObject`); `VerifyObjectPermission` for GetObject and HeadObject (`S3GetObject`/`S3GetObjectVersion`), GetObjectAttributes (`S3GetObject` on both releases; Tentacle's `s3GetObjectAttributes` is or-ed, [`[T]:6361-6390`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6361-L6390), so `S3GetObject` alone decides), GetObjectAcl/PutObjectAcl, Get/Put/DeleteObjectTagging (`…Version…` with `versionId`), ListParts (`S3ListMultipartUploadParts`) and UploadPart's `verify_permission`? No: UploadPart is `RGWPutObj` with `uploadId` → `VerifyBucketPermission`.
- CopyObject's source: `op.VerifyBucketPermissionIn(ctx, r, S3GetObject(Version), acl.PermFor(...), srcRec, srcKey)` (RGWCopyObj, [`:5453-5460`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5453-L5460); W Task 10); UploadPartCopy's source: `op.VerifyObjectPermissionIn(ctx, r, ..., srcRec, srcState)` (RGWPutObj, [`:3955-3963`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3955-L3963); P Task 8); DeleteObjects' per key: `op.VerifyBucketPermissionIn(ctx, r, ..., r.BucketRec, key)` (W Task 10). Never a copy of the request with `BucketRec` swapped: that moves the public-access block and requester-pays to the source bucket.
- Multipart ops that read the object ACL (ListParts, `read_obj_policy` [`:398-418`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L398-L418)) pass the upload's meta object attrs as the `obj` of `VerifyObjectIn` (P sets them on `r.ObjState`, which is the same thing through `VerifyObjectPermission`).
- Building the resolver (M Task 7, W Task 11, P Task 9, through M's `s3.userResolver(r)` helper): `authz.UserResolver{Users: r.Env.Users, Accounts: r.Env.Accounts}` — `op.AccountStore` has `AccountName` from A Task 1, so this compiles in wave 1; on the real driver account grantees resolve once N Task 7 replaces the stub.
- Call `VerifyObjectPermission` even when `Init` found no object; treat `op.ErrNoSuchKey` from it as the 404 (D-Z5).
- A bucket's `user.rgw.acl` is written by CreateBucket as `BuildDefaultACL(...).Encode(e, release)`; the object's by PutObject, CompleteMultipart and CopyObject; PutBucketAcl/PutObjectAcl through `BuildACL`; PutBucketPolicy stores `ParseBucketPolicy(...).Text` under `user.rgw.iam-policy`; DeleteBucketPolicy removes the attr; GetBucketPolicy returns the attr verbatim as `application/json` or `op.ErrNoSuchBucketPolicy` with message `The bucket policy does not exist` when absent or empty ([`rgw_op.cc:8146-8167`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8146-L8167)); Put/Get/DeleteBucketTagging and the object tagging ops use `tags.ParseXML(body, tags.MaxBucketTags|MaxObjectTags)`, `Set.Encode`, `tags.Decode` and `Set.MarshalXML`, with `op.ErrNoSuchTagSet` for a bucket without tags ([`:1159-1169`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1159-L1169)) and an empty `TagSet` for an object without tags ([`rgw_rest_s3.cc:754-771`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L754-L771)).
- Suspended buckets and unparsable bucket policies are Z's: `Init` loads the record and leaves the decision to `Verify*`.

---

## Self-review

**Spec coverage.** §9 "ACL, policy, quota and tenant authorization": ACL evaluation (Tasks 6, 10), bucket and identity policy (Tasks 1-5, 10), tenants (Tasks 2, 5, 10: ARN accounts, principal tenants, CreateBucket's tenant guard, `tenant:bucket` requests through `r.Tenant`), quota (D-Z8, G's `CheckQuota`). "ACL, policy and tagging subresources": the types and both XML directions (Tasks 7, 8), the parse and render helpers the storing ops call (Task 11). §8's stored-form rule: `RGWObjTags` proven by corpus goldens (Task 8), `PublicAccessBlockConfiguration` by hand fixtures (no corpus objects exist), the user-policy attrs by encode-decode specs (Task 5); `user.rgw.acl` was phase 0's. Decision D6: Task 9's `identityPolicies` and Task 10's precedence specs; owner decision 6 (IAM group members refused, D-Z2): Task 10's `deniedAsGroupMember`, its specs and its `docs/exclusions.md` entry, Task 11's end-to-end case; owner decision 12c (ACL escaping, D-Z7): Task 7's rendering and its `docs/exclusions.md` entry, Task 12's registry entry. Unit Z's gate in the index (`00-index.md`, "The nine units"): radosgw's own policy cases (Tasks 3-5), s3-tests groups (Task 12).

**Placeholder scan.** Task 10's stored-policy corruption case tells the implementer to read `Strategy::apply` and pin the observed mapping; the expected value is stated (`ErrAccessDenied`) so the spec is complete either way. Task 12's test-name list defers to T-b's baseline for exact names, which is the authority the plan cites. No "TBD", "similar to", or unshown code steps remain; Tasks 3 and 4 give their rules as numbered transcriptions with the C++ line for each instead of Go bodies, which is the level a transcription needs.

**Type consistency.** `policy.Env` is `map[string][]string` in Tasks 2, 3, 9, 10. `acl.Identity` (Task 6) and `policy.Identity` (Task 4) are both implemented by `authz.identity` (Task 9). `acl.PermFor` returns `Permission` and is passed as G's `perm` (D-Z3, contract appendix). `VerifyBucketIn`'s `key` is `meta.ObjKey` as G's `Request.Object` is, and both `In` signatures are `op.Authorizer`'s verbatim. `ParseOptions.Tenant *string` matches `PolicyTenant`'s return. `tags.Attr` is used by `authz.addTags`. `Semantics` is built once in `New` from `cfg.Release` and passed to every `Eval`/`IsPublic`/`Parse`.

**Review Focus.** 1 → Task 10 stored identity policies; 2 → Task 10 missing object; 3 → Task 5 (`Parse rejects a Tentacle-only action on Squid`) and Task 11 (`ParseBucketPolicy`); 4 → Task 6 (`ignorePublicACLs`) and Task 10 (`blocked`); 5 → Task 9 (`PermMask`) and Task 10 (subuser PUT); 6 → Task 10 (IAM group members) and Task 11 (the group case with and without the admin override). Each line has a test in the owning task.

**G-contract findings for the caller** (not changed here, reported): (a) G's `policy.Action` s3 block is ceph `main`'s, not v19.2.6's: it carries `S3PutAccountPublicAccessBlock` and `S3GetAccountPublicAccessBlock`, which no shipped release knows, and seven Tentacle-only names; harmless as numbering (the bitset is never persisted) but `Known` must gate them, and G's "82 s3: rows" count is main's. (b) `Authorizer`'s `perm` duplicates `op_to_perm` (D-Z3). (c) `op.Run` overrides only `ErrAccessDenied` for an admin; radosgw overrides any negative `verify_permission` result ([`rgw_process.cc:225-236`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L225-L236)), e.g. `ERR_MFA_REQUIRED` (phase 2). (d) `Identity.Admin` must be set by unit A as `admin || system` for `Run`'s override to equal `is_admin_of`/`is_admin()` ([`rgw_auth.cc:1048-1051`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1048-L1051), [`[T]:1068-1071`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth.cc#L1068-L1071)); `Identity.System` separately drives the Squid-only bypasses. (e) `Identity` has no `perm_mask`; Z derives it from `User.SubUsers[SubUser]` (Task 9), so no contract change is needed. (f) `s3.Config`/`cephconf` read none of `rgw_policy_reject_invalid_principals`, `rgw_enforce_swift_acls`, `rgw_remote_addr_param`, `rgw_trust_forwarded_https`, `rgw_acl_grants_max_num`; `authz.ConfigFrom` reads them through `cephconf.Options`, which G's contract allows ("every unit may read through `Env.Conf`").
