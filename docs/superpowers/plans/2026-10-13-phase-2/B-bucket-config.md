# Phase 2 unit B: bucket configuration

Threat model: a client adversary at the security boundaries PP-14 names, which get adversarial
review: authentication (BC-3's preflight, BC-5, BC-6's options_cors route, BC-31), authorization
(BC-4, BC-18, BC-28c, BC-28e, BC-32) and the parsers of a client's documents (BC-2, BC-10, BC-17,
BC-23, BC-28a, BC-29, BC-30, BC-30b); everything else here, the configuration storage, the website renderer
and the recording tasks, defends against honest mistakes, and every write keeps the bucket instance
radosgw-readable.

## Scope

The spec's phase 2 items (§9) in this unit, in its words: "CORS with preflight, static website,
the encryption configuration ops behaving as an unconfigured radosgw, ownership controls,
public access block, policy status, POST object".

| Item | S3 surface | Tasks | Stored form |
|---|---|---|---|
| CORS | Get/Put/DeleteBucketCors (`?cors`); preflight `OPTIONS` at bucket and object scope; `Access-Control-*` and `Vary` on ordinary responses; the non-preflight refusal | BC-2 to BC-8 | bucket attr `user.rgw.cors`, RGWCORSConfiguration (1,1) |
| Static website | Get/Put/DeleteBucketWebsite (`?website`, gated by `rgw_enable_static_website`); the website endpoint: index and error documents, routing rules, RedirectAllRequestsTo, `x-amz-website-redirect-location`, HTML errors | BC-9 to BC-16 | `RGWBucketInfo.website_conf` and `has_website`, RGWBucketWebsiteConf (2,1) |
| Public access block, policy status | Get/Put/DeletePublicAccessBlock; GetBucketPolicyStatus | BC-17 to BC-22 | bucket attr `user.rgw.public-access` (1,1); policy status is computed |
| Encryption configuration | Get/Put/DeleteBucketEncryption; the bucket default's effect on PutObject, CopyObject, CreateMultipartUpload, UploadPart and POST object | BC-23 to BC-27 | bucket attr `user.rgw.sse-s3.policy`, RGWBucketEncryptionConfig (1,1) |
| Ownership controls | Get/Put/DeleteBucketOwnershipControls; CreateBucket's `x-amz-object-ownership`; enforcement | BC-28a to BC-28f | bucket attr `user.rgw.ownership-controls`, v21.1.2's (1,1){u8} |
| POST object | browser upload at bucket and object scope: the form, the SigV2 and SigV4 form credentials, the policy document, the write, the four responses | BC-29 to BC-34 | the object, as PutObject writes it |

Not in this unit: the route-set mechanism (S2, which replaces the report's BC-1), the
`IsPublic` judgment (S3), the s3-tests include list's machinery (the T unit, D20), POST
object's checksum computation (WP-5, WP-6), and its versioned write (V11). This unit adds no
fakerados work: every write here is a bucket-instance rewrite or a PutObject the emulator
already serves.

## Decisions

Index decisions this unit applies: D15 to D20, D21 and D22 for POST object's checksum field,
and P4 (the owner, 2026-10-13).

| Id | Decision | Reason |
|---|---|---|
| B1 | Every S3 response carries the CORS headers once the request's bucket is loaded, error responses included (D15). One exclusion lists the routes radosgw omits. | AWS's per-request model; no per-release route table to keep. |
| B2 | PutBucketWebsite's unparseable body or missing `WebsiteConfiguration` is 400 MalformedXML, not radosgw's InvalidArgument. The block document keeps radosgw's missing namespace, and PolicyStatus and the block keep lower-case booleans (D16). | The error code a client sees follows AWS; document bytes that SDKs ignore stay byte-comparable with radosgw. |
| B3 | GetBucketPolicyStatus and the block's enforcement judge a policy with S3's `IsPublic` alone (D17). | One AWS-correct judgment on both releases. |
| B4 | A signed preflight's signature is not compared, as radosgw and AWS do (D18). The registry entry records that phase 3's rate limiter must neither charge a preflight to its key's user nor apply that user's limit or exemption. | The presigned CORS tests need it; the rate limit is the security half: radosgw's limiter answers a forged preflight 503 SlowDown by the key user's limit ([rgw_process.cc:108-134](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L108-L134), [rgw_process.cc:108-134](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L108-L134)), and never for a system key, nor on Squid for an admin key ([rgw_process.cc:96-98](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L96-L98), [rgw_process.cc:97](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L97)). |
| B5 | PutBucketEncryption is validated as AWS documents: SSEAlgorithm one of `AES256`, `aws:kms`, `aws:kms:dsse`, `aws:fsx`, `aws:backup`; KMSMasterKeyID only with `aws:kms` or `aws:kms:dsse`; 400 MalformedXML otherwise (D19). The stored form is unchanged. | AWS's API reference; a stored default neither gateway can serve fails a write on both, as on an unconfigured radosgw. |
| B6 | The six encryption-configuration s3-tests, and BC-25's three, run through the T unit's include list (D20); BC-27 adds their lines. The eight ownership tests carry only `object_ownership`, so BC-28f lifts that marker. | The T unit owns the selection; each area owns its own lines. |
| B7 | Ownership controls are built to v21.1.2's stored form and enforced (P4, the owner, 2026-10-13). | The spec names the item; an upgraded zone's radosgw reads that form; the floors' fall-through can delete a bucket (defect 11). |
| B8 | CORS rule selection is main's `match_rule`: the first rule matching origin, method and, for a preflight, every requested header. A non-preflight request with an Origin no rule allows is refused, as main's `verify_cors_match` does. | AWS documents both; s3-tests expect the refusal; defects 1 and 2. |
| B9 | Each bucket-attr write sets only its own attr on each try and is authorized again on each retry, as phase 1's `retryBucketWrite` does. | Defects 4 and 5: radosgw writes back the attrs the request started with, and does not re-authorize. |
| B10 | GetPublicAccessBlock on a bucket without a block sends the 404 error document alone. | Defect 6: radosgw sends a document past the Content-Length. |
| B11 | POST object refuses a suspended user and a public canned ACL under BlockPublicAcls, counts its body in the usage log, and ends a part only at an RFC 2046 delimiter. | Defects 8, 9, 10 and 18. |
| B12 | POST object's `$key` condition is matched as AWS documents it. The task quotes AWS's page with its URL in its notes; when the page does not settle it, rgw-go keeps radosgw's match against the key with `${filename}` replaced. | Review Focus 5. The answer is not written from memory (PP-2). |
| B13 | Tentacle's POST policy exception for checksum fields applies on both releases. | D22 serves single-object checksums on Squid too. |
| B14 | Where the floors differ only in gateway-side request parsing, Tentacle's behaviour applies on both releases: the SigV2 preflight's Access-Control-Request-Method rule, and the website flag for a request with no Host. | The spec's "feature level Tentacle where gateway-side" (§3). |
| B15 | SSE-S3 and SSE-KMS writes answer as an unconfigured radosgw does, recorded from a running one (BC-27 (a)) before BC-26's specs pin them. SSE-C stays 501 until phase 4. A default naming no algorithm refuses nothing. | The spec's "behaving as an unconfigured radosgw"; the final status is known only by source reading. |
| B16 | A refusal that reads the bucket's stored encryption default comes after authorization; a refusal from the request's own headers comes where radosgw makes it, before. | Phase 1's rule that a refused requester learns nothing of what the store holds ("PutObject and CopyObject authorize a request before they read what the store holds for it"). |
| B17 | A website configuration stored at v1 is decoded and rewritten as v2 on any instance rewrite. The admin metadata JSON is radosgw's lossy `website_conf` form. | Both are what radosgw does to the shared bucket instance. |

## Seams

**Consumed from other units.**
- S2: one route-set function per feature, each in its own file, returning `[]routeSet`. The
  files are `internal/s3/{cors,website,publicaccess,encryption,policystatus,ownership,postobject}.go`.
  Only `postobject.go`'s function takes the authenticator. This unit adds no `op.Env` field:
  its ops use `Env.Buckets`, `Env.Objects`, `Env.Conf` and `Env.Authz`.
- S3: `func (p *Policy) IsPublic() bool` in `internal/policy`, the same answer on both
  releases. BC-20 calls it directly. BC-18 calls nothing of S3, but its specs that a block it
  writes is enforced (BlockPublicPolicy, RestrictPublicBuckets) assert S3's judgment.
- WP-1: `cksum.ParseName(string) (cksum.Type, bool)`, which decides which form fields are
  checksum fields for BC-30's exception.
- WP-5 and WP-6: `op.PutParams.Checksum *op.ChecksumRequest`, built by WP-6's request parser
  from the form's `x-amz-checksum-*` fields in place of headers, so D21's refusals hold for
  POST object (BC-32).
- V11: `op.VersionForWrite(r) string`. Until V11 lands, BC-32 calls the versioned-bucket
  refusal PutObject calls today, and V11, which removes that refusal, replaces its call in
  `postobject.go` as it does at every other site.

**Produced inside this unit** (named here so tasks can run in parallel):
- `internal/cors` (BC-2): `Decode(b []byte) (*Configuration, error)`,
  `(*Configuration).Match(origin, method string, reqHeaders []string) *Rule`,
  `(*Rule).ResponseHeaders(origin, method string, reqHeaders []string, authorized bool) Headers`.
- `meta.WebsiteConf` (BC-9), the field type of `BucketInfo.Website`.
- `internal/website` (BC-10): `ParseXML(body []byte, maxRules int) (meta.WebsiteConf, error)`,
  `EffectiveKey(conf meta.WebsiteConf, key string, isFile bool) (string, bool)`,
  `Redirect(conf meta.WebsiteConf, key string, status int, proto, host string) (url string, code int, ok bool, err error)`,
  and its halves `Match(conf meta.WebsiteConf, key string, status int) (meta.RoutingRule, bool)`
  and `Apply(r meta.RoutingRule, proto, host, key string) (url string, code int, err error)`,
  whose one error is `website.ErrUnsafeRedirect`.
- `op.Request.Website bool` and `op.Request.Redirect string` (BC-13).
- `internal/sse` (BC-23): the three configuration structs, their XML and `Validate`.
- `op.EncryptionDefaults(r *Request, asked map[string]string) (Encryption, error)` and
  `op.Request.CryptAttrs` (BC-26), which BC-32 calls for POST object.
- `acl.Ownership` with `OwnershipOf(attrs map[string][]byte) Ownership` (BC-28a), and
  `acl.CannedFor(owner, bucketOwner acl.Owner, own acl.Ownership, canned string) (acl.Policy, error)`
  (BC-28e).
- `newFormReader(body io.Reader, contentType string, maxLine int) (*formReader, error)`,
  `(*formReader).Fields() (formFields, *formFile, error)`, `(*formReader).Drain() error` (BC-29).
- `postpolicy.Parse(doc []byte) (*Policy, error)`, `(*Policy).Check(env *Env, now time.Time) error`,
  and its `MinLength` and `MaxLength` (BC-30).
- `op.BrowserUploadCredentials{Algorithm, AccessKey, Credential, Signature, SecurityToken, Policy string}`
  and `Authenticator.AuthenticateBrowserUpload(ctx, op.BrowserUploadCredentials) (*op.AuthResult, error)` (BC-31).

**Produced for other units.** The T unit receives this unit's include-list lines (BC-27), the
`object_ownership` marker's removal (BC-28f) and its deselect-line removals (BC-8, BC-22,
BC-34). The phase 2 gate receives the cross-gateway cases of BC-8, BC-16, BC-22, BC-27, BC-28f
and BC-34.

## Hot files

| File | Tasks here | Other units | Rule |
|---|---|---|---|
| `internal/s3/handler.go` | BC-15 (website-mode error path), BC-33 (the byte counter's condition) | S2 (binding) | merge S2, then BC-15, then BC-33 |
| `internal/s3/dispatch.go` | BC-15 (one call line at the top of `Dispatch`; the website dispatch lives in `website_serve.go`), BC-28b (`ownershipControls` rows) | versioning | line-local; never in parallel |
| `internal/s3/headers.go`, `internal/s3/errors.go` | BC-7 (`SetCommonHeaders`, `SetPreflightHeaders`), BC-15 (the HTML error variant) | - | one owner each |
| `internal/op/op.go` | BC-4 (one call into `corsmatch.go`) | - | single owner |
| `internal/op/request.go` | BC-13 (`Website`, `Redirect`), BC-26 (`CryptAttrs`) | versioning | additive fields, each task's in its own PR |
| `internal/op/getobject.go` | BC-14 (one hook field, one call; the body in `website_get.go`) | versioning, WP-12 | line-local |
| `internal/op/{putobject,initmultipart,uploadpart}.go` | BC-26 | WP-6, V11, L5b | BC-26 after WP-6 (index Order 3) |
| `internal/op/copyobject.go` | BC-26 (b) | V11 | never in parallel |
| `internal/s3/{putobject,copyobject}.go`, `internal/s3/multipart.go` | BC-26; BC-13 (b) (`completeLocation`) | WP-7, WP-13, versioning, L5b, C8 | One call line per site, so whichever lands first, the later rebases; BC-26's op-file changes keep their order after WP-6 and V11, which share function bodies (index Order) |
| `internal/authz/{env,load,write,evaluator}.go` | BC-18 (the block ops' check in `evaluator.go`), BC-26 (`cryptKeys` in `env.go`), BC-28e | S3 (the `IsPublic` callers), S5 (the ACL checks) | BC-18 and BC-28e after S3; BC-18 after S5 |
| `internal/op/bucketsubres.go`, `objectattrs.go`, `bucketops.go` | BC-28c | C6 (moves GetBucketLifecycle out of `bucketsubres.go`), L2 (b) (CreateBucket) | BC-28c (b) after L2 (b); line-local otherwise |
| `internal/s3/bucket.go` (the CreateBucket handler) | BC-28d (one header read) | L2 (b) | BC-28d after L2 (b). S2 moves no existing binding; each feature task moves its own into its route set |
| `internal/op/postobject.go` | BC-32 (owner), BC-28e (b) (the canned ACL call) | V11 (`VersionForWrite`) | BC-28e (b) after BC-32; whichever of V11 and BC-32 lands second switches the versioned-bucket refusal to `VersionForWrite` |
| `internal/meta/{bucket_info,bucket_json,dump_bucket}.go` | BC-9 | L1 leaves them unedited | single owner |
| `internal/auth/{sigv4,sigv2,verifier}.go`; `internal/s3/auth.go` | BC-5; BC-31 (the interface, and a new `browserupload.go`) | WP-3 (`chunked.go`, `payload.go`) | one owner each |
| `hack/s3tests` lists, ledgers, known-differences files | BC-8, BC-22, BC-27, BC-28f, BC-34 | every unit's recording; WP-22, WP-23 | one at a time through the controller; after WP-22 each edits only its own lines in the Go selection's lists |
| `docs/exclusions.md`, `docs/ceph-upstream-bugs.md` | nearly every task | every unit | the controller's merge order |

## Tasks

Every op here follows phase 1's bucket-subresource pattern (`internal/op/bucketsubres.go:56-90`):
Init loads the bucket and its attrs, so a missing one is NoSuchBucket before the permission
check, as radosgw's `init_permissions` does ([rgw_process.cc:174](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L174),
[rgw_process.cc:174](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L174));
VerifyPermission authorizes the op's action through `verifyBucketAction`, with bucket tags
where phase 1's tag rules list the action (`internal/authz/tagrule.go:120-143`); a write goes
through `retryBucketWrite`. The metadata-master forward is a no-op on one zone, and the code is
identical at both tags apart from the forward call's `s->err` argument at v20.2.4, unless a
behaviour line says otherwise. Non-cluster specs run on counterfeiter fakes and memstore; none
relies on fakerados behaviour beyond what it serves today.

**CORS: BC-2 to BC-8.**

### Task BC-2: CORS types

- **Layer:** library. **Files:** create `internal/cors/` (types, encoding, XML, matching).
  **Cluster:** no.
- **Consumes:** `internal/denc`, `internal/xmltext`. **Produces:** `Configuration` and `Rule`;
  `Decode(b []byte) (*Configuration, error)` and `(*Configuration).Encode() []byte`;
  `ParseXML(body []byte) (*Configuration, error)` and `(*Configuration).MarshalS3XML() []byte`;
  `(*Configuration).Match(origin, method string, reqHeaders []string) *Rule`;
  `(*Rule).ResponseHeaders(origin, method string, reqHeaders []string, authorized bool) Headers`.
- **Behaviour.** The four ceph files are byte-identical at the two tags, so each citation holds
  at both.
  1. **Encoding.** RGWCORSRule is ENCODE_START(1,1): max_age u32, allowed_methods u8, id, the
     allowed-header set, the allowed-origin set, the exposable-header list
     ([rgw_cors.h:63-82](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L63-L82), [rgw_cors.h:63-82](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.h#L63-L82)). RGWCORSConfiguration
     is (1,1) over the rule list ([rgw_cors.h:102-111](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L102-L111), [rgw_cors.h:102-111](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.h#L102-L111)).
     The sets encode sorted, as `std::set<std::string>` orders them; a rule with no method and
     the invalid max age 0xffffffff round-trip.
  2. **Method flags** GET 0x1, PUT 0x2, HEAD 0x4, POST 0x8, DELETE 0x10, COPY 0x20, and
     CORS_MAX_AGE_INVALID ([rgw_cors.h:22-35](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L22-L35), [rgw_cors.h:22-35](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.h#L22-L35)).
     A request's method maps by exact name, never to COPY
     ([rgw_cors.h:137-147](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L137-L147), [rgw_cors.h:137-147](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.h#L137-L147)).
  3. **Parsing.** Each refusal is MalformedXML: an AllowedMethod other than GET, POST, DELETE,
     HEAD, PUT or COPY in any case; an ID over 255 bytes; no AllowedOrigin; an AllowedOrigin or
     AllowedHeader that is empty or holds more than one `*`; a MaxAgeSeconds with trailing
     junk. A MaxAgeSeconds of 2^32 or more is stored invalid, and a rule with no AllowedMethod
     is accepted ([rgw_cors_s3.cc:76-164](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors_s3.cc#L76-L164),
     [rgw_cors_s3.cc:76-164](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors_s3.cc#L76-L164)). A configuration needs one
     rule ([rgw_cors_s3.cc:177-188](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors_s3.cc#L177-L188), [rgw_cors_s3.cc:177-188](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors_s3.cc#L177-L188)).
  4. **Rendering.** `CORSConfiguration` in the S3 namespace; per rule the ID when non-empty,
     AllowedMethod in the order GET, PUT, DELETE, HEAD, POST, COPY, the origins and headers
     sorted, MaxAgeSeconds when valid, ExposeHeader in stored order
     ([rgw_cors_s3.cc:32-74](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors_s3.cc#L32-L74),
     [rgw_cors_s3.cc:32-74](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors_s3.cc#L32-L74),
     [rgw_cors_s3.cc:166-175](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors_s3.cc#L166-L175), [rgw_cors_s3.cc:166-175](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors_s3.cc#L166-L175)).
  5. **Matching.** An origin matches when the set holds `*`, the origin itself, or a member
     with one `*` whose prefix and suffix bracket it; origins compare case-sensitively, headers
     lower-cased ([rgw_cors.cc:109-173](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.cc#L109-L173), [rgw_cors.cc:109-173](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.cc#L109-L173)).
     Expose headers join with `,`, a newline escaped as `\n`
     ([rgw_cors.cc:175-184](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.cc#L175-L184), [rgw_cors.cc:175-184](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.cc#L175-L184)).
  6. **Rule selection (B8).** `Match` is main's: the first rule, in stored order, matching the
     origin, the method and, for a preflight, every requested header
     ([rgw_cors.cc:242-290](https://github.com/ceph/ceph/blob/cc1dcecee08/src/rgw/rgw_cors.cc#L242-L290)). The floors
     take the first rule matching the origin alone and test its method afterwards
     ([rgw_cors.cc:186-194](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.cc#L186-L194),
     [rgw_cors.cc:186-194](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.cc#L186-L194)).
  7. **Response headers.** Access-Control-Allow-Origin is `*` when the request is unauthorized
     (no Authorization header) and the rule's origins include `*`, else the Origin as sent;
     Vary: Origin unless that value is `*`; Allow-Methods is the method; Allow-Headers the
     requested headers the rule allows, comma-joined; Expose-Headers; Max-Age when valid
     ([rgw_op.cc:1541-1600](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1541-L1600),
     [rgw_op.cc:1778-1837](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1778-L1837);
     [rgw_rest.cc:526-554](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L526-L554),
     [rgw_rest.cc:531-559](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L531-L559)).
- **Specs:**
  - the ceph-object-corpus RGWCORSRule goldens (18.2.0 and 19.2.0-404-g78ddc7f9027) decode to
    ceph-dencoder's JSON and re-encode byte-identically;
  - a hand-built RGWCORSConfiguration vector (BC-8 later compares it with radosgw's bytes);
  - every parse refusal as a table row;
  - test_cors_origin_response's origin table reproduced in Go;
  - a configuration on which `host_name_rule` and `match_rule` choose different rules;
  - MaxAgeSeconds 2^32; an expose header holding a newline; a rule with no method.
- **Exclusions and registry:** new exclusion "CORS rules are matched as Amazon S3 matches
  them" (defect 2); new registry entry for defect 2 (S3-correctness, unreproduced, fixed on
  main by cc1dcecee08, PR #69190).
- **Depends on:** nothing. **PR:** one. **Brief:** about 200 lines.

### Task BC-3: CORS ops

- **Layer:** op. **Files:** create `internal/op/cors.go`. **Cluster:** no.
- **Consumes:** BC-2's `Decode`, `Match`; BC-5's preflight marker. **Produces:** `AttrCORS`
  (`user.rgw.cors`); `GetBucketCORS{Config}`, `PutBucketCORS{Params}` (a body hook, as
  PutBucketTagging's), `DeleteBucketCORS`, and `OptionsCORS` with its results: the rule, the
  origin, the method and the allowed requested headers.
- **Behaviour:**
  1. **Get.** Action s3:GetBucketCORS, with bucket tags
     ([rgw_op.cc:6049-6060](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6049-L6060),
     [rgw_op.cc:6716-6727](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6716-L6727)). No attr is
     ErrNoCORSFound; an attr that does not decode is 500, radosgw's EIO
     ([rgw_op.cc:1483-1512](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1483-L1512),
     [rgw_op.cc:1720-1749](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1720-L1749);
     [rgw_op.cc:6062-6073](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6062-L6073),
     [rgw_op.cc:6729-6740](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6729-L6740)).
  2. **Put.** Action s3:PutBucketCORS
     ([rgw_op.cc:6075-6086](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6075-L6086),
     [rgw_op.cc:6742-6753](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6742-L6753)). It writes only
     `user.rgw.cors` on each try (B9), where radosgw merges the attrs the request started with
     ([rgw_op.cc:6103-6107](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6103-L6107),
     [rgw_op.cc:6770-6774](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6770-L6774)).
  3. **Delete.** Authorized as s3:PutBucketCORS, "No separate delete permission"
     ([rgw_op.cc:6110-6122](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6110-L6122),
     [rgw_op.cc:6777-6789](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6777-L6789)). It removes the attr,
     and a bucket without one succeeds too
     ([rgw_op.cc:6124-6153](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6124-L6153),
     [rgw_op.cc:6791-6820](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6791-L6820)).
  4. **Options.** No permission check ([rgw_op.h:1756](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1756),
     [rgw_op.h:1912](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1912)). Execute, in order: an undecodable configuration is 500;
     no Origin is 400 InvalidArgument; no Access-Control-Request-Method is 400 InvalidArgument;
     no configuration is 403 AccessDenied; no rule from `Match` is 403
     ([rgw_op.cc:6159-6207](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6159-L6207),
     [rgw_op.cc:6826-6874](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6826-L6874)). The op types are
     radosgw's, and so are the masks of get (read) and of put and delete (write)
     ([rgw_op.h:1697-1764](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1697-L1764), [rgw_op.h:1850-1920](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1850-L1920)).
     Options' `Name` is `op.OptionsCORSName`, radosgw's `options_cors`
     ([rgw_op.h:1761](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1761),
     [rgw_op.h:1917](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1917)): BC-5's
     guard, `op.RefuseUnverified`, lets only that op run an identity granted without a signature
     compare, so under any other name `op.Run` refuses the preflight with 500.
     A preflight's identity is its key's user, with no signature compared (BC-5), so its answer
     must be the same for every key that resolves. Options therefore departs from radosgw in
     three ways, each an exclusion BC-3 records, and BC-3 rules on the tenant:
     - **No op mask check.** radosgw's mask for options is read
       ([rgw_op.h:1763](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1763),
       [rgw_op.h:1919](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1919)), and
       `verify_op_mask` checks it against the key's user
       ([rgw_process.cc:208](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L208),
       [rgw_process.cc:208](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L208);
       [rgw_op.cc:1017-1035](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1017-L1035),
       [rgw_op.cc:1216-1234](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1216-L1234)),
       so a forged preflight reveals that the key's user lacks the read bit: 403 AccessDenied
       where another key gets the CORS answer. Options asks for no op mask.
     - **No x-amz-request-charged.** radosgw adds it to the answer when the identity does not
       own a requester-pays bucket
       ([rgw_rest.cc:597-601](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L597-L601),
       [rgw_rest.cc:602-606](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L602-L606)),
       which reveals whether the key's user owns it. BC-6's route sends the answer without it.
     - **Usage as anonymous.** radosgw charges the key's user as payer on a requester-pays
       bucket, and files the entry under it as owner when no bucket is named
       ([rgw_log.cc:203-219](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_log.cc#L203-L219),
       [rgw_log.cc:203-219](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_log.cc#L203-L219)).
       A preflight's usage is accounted as an anonymous request's, keyed on BC-5's preflight
       marker, so no entry is charged to the key's user.
     - **Tenant.** radosgw resolves a bucket name that names no tenant under the identity's tenant
       ([rgw_rest_s3.cc:4995-4998](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4995-L4998),
       [rgw_rest_s3.cc:5525-5528](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5525-L5528);
       [rgw_bucket.cc:127-130](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket.cc#L127-L130),
       [rgw_bucket.cc:127-130](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_bucket.cc#L127-L130)),
       which is why its preflight looks the key up at all (ceph commit fe15b52edb5). BC-3 rules,
       with a spec, whether a preflight's bucket resolves under the key user's tenant.
       Constraint: the answers for two keys whose users differ only in tenant differ only where
       an anonymous request's answers for the same bucket name would (an anonymous request
       names a tenant only explicitly, as `t:b`), or the departure is recorded as an exclusion.
       Reason: the tenant is a property of the key's user.
- **Specs:**
  - each op's error order with counterfeiter fakes;
  - a PutBucketCors that loses a race to a PutBucketAcl keeps the winner's ACL;
  - a retry runs VerifyPermission again and writes nothing for a requester the new ACL refuses;
  - OptionsCORS on a bucket whose rules allow the origin but not the method;
  - the invariant table: OptionsCORS, run with a forged preflight's identity from a plain, a
    tenanted, an account, a suspended and a system key, and from a key whose user lacks the read
    op mask, on one requester-pays bucket that every key resolves alike and none of their users
    owns, gives each the same error or the same results, and no usage entry `LogUsage` writes
    for them is charged to any of them; BC-6 runs the table end to end;
  - the tenant ruling: a tenanted key's preflight of a bucket name without a tenant, beside an
    anonymous preflight of that name with and without the tenant.
- **Exclusions and registry:** extends "A retried PutBucketPolicy keeps what the write it lost
  to changed" and its registry entry with PutBucketCors (defect 4, data corruption,
  unreproduced); extends "A PutBucketAcl that loses a race is retried, and every retried ACL,
  policy or tagging write is authorized again" with the CORS writes (defect 5); adds behaviour
  4's three departures and its tenant ruling, with any departure from the tenant constraint,
  to BC-5's exclusion "A preflight's answer says nothing of its key's user". The CORS writes
  also join "A retried bucket write stops when the name names another bucket", citing their
  retries ([rgw_op.cc:6103-6107](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6103-L6107),
  [rgw_op.cc:6133-6152](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6133-L6152),
  [rgw_op.cc:6770-6774](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6770-L6774),
  [rgw_op.cc:6800-6819](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6800-L6819)).
- **Depends on:** BC-2, BC-5 (the preflight marker). **PR:** one. **Brief:** about 180 lines.

### Task BC-4: The non-preflight CORS refusal

- **Layer:** op. **Files:** create `internal/op/corsmatch.go`; modify `internal/op/op.go` (one
  call in `Run`). **Cluster:** no.
- **Consumes:** BC-2's `Decode` and `Match`, BC-3's `AttrCORS`. **Produces:** nothing outside
  the package.
- **Behaviour** (B8, main's `verify_cors_match`,
  [rgw_op.cc:2088-2133](https://github.com/ceph/ceph/blob/292995374d9/src/rgw/rgw_op.cc#L2088-L2133), called at
  [rgw_process.cc:295-299](https://github.com/ceph/ceph/blob/292995374d9/src/rgw/rgw_process.cc#L295-L299); neither floor has it):
  1. It runs in `Run` after the op mask and before the permission verdict, so an admin's
     override does not lift it.
  2. A request that is not options_cors, carries a non-empty Origin, and has a loaded bucket
     whose configuration no rule matches for the request's own method (not
     Access-Control-Request-Method) is 403 AccessDenied.
  3. No configuration, or no Origin, passes. An undecodable configuration is the 500 that
     radosgw's read gives.
- **Specs:** an Origin that matches and one that does not; the method taken from the request;
  an admin refused; a service-scope request untouched; an op error before the op mask answered
  first; POST object and a website GET refused alike.
- **Exclusions and registry:** new exclusion "A request whose Origin no CORS rule allows is
  refused" (defect 1); new registry entry for defect 1 (S3-correctness, unreproduced, fixed on
  main by 4e056f98e78, 8d652cd24c1 and 7f147f02058, in no tag).
- **Depends on:** BC-2, BC-3. **PR:** one. **Brief:** about 100 lines.

### Task BC-5: Preflight signing

- **Layer:** auth. **Files:** modify `internal/auth/sigv4.go`, `sigv2.go`, `verifier.go`.
  **Cluster:** no.
- **Consumes / Produces:** nothing new; the canonical method's input changes for OPTIONS.
- **Behaviour:**
  1. For an OPTIONS request the canonical method is Access-Control-Request-Method. A missing
     one, or one other than exactly GET, POST, PUT, DELETE or HEAD, is 400 InvalidArgument,
     after the credentials and canonical headers are read and before the key is looked up
     ([rgw_auth_s3.cc:705-732](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L705-L732),
     [rgw_auth_s3.cc:1749-1776](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1749-L1776)).
  2. **Differs:** v20.2.4 applies the rule to SigV2 too
     ([rgw_auth_s3.cc:258](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L258)); v19.2.6 signs a SigV2 OPTIONS over OPTIONS. rgw-go
     applies Tentacle's rule on both releases (B14).
  3. Once the key's user and account load and the key is in the user's record, the signature
     is not compared (B4; [rgw_rest_s3.cc:6395-6401](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L6395-L6401),
     [rgw_rest_s3.cc:6925-6931](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6925-L6931)). An unknown key stays 403 InvalidAccessKeyId.
- **Specs:** a presigned OPTIONS without the header is 400 before an unknown key's 403; a wrong
  signature passes; an unknown key is 403; SigV2 on each release; an unknown method name.
- **Exclusions and registry:** retires "A signed OPTIONS request is verified over OPTIONS and
  answered 501 until CORS lands"; new exclusion for the SigV2 rule on Squid (B14); updates the
  registry entry "radosgw grants a signed CORS preflight without comparing its signature" with
  rgw-go's choice and the phase 3 rate-limit constraint (B4).
- **Depends on:** nothing. **PR:** one. **Brief:** about 120 lines.

### Task BC-6: CORS routes

- **Layer:** handler. **Files:** S2's `internal/s3/cors.go`. **Cluster:** no.
- **Consumes:** BC-3's ops, each with its `StorageClass` input; BC-7's preflight seam
  (`SetPreflightHeaders`, and `SetCommonHeaders`' rule for an OPTIONS response); phase 1's
  `readParamBody` (`internal/s3/bucket.go:135`) and `writeXML` (`internal/s3/xml.go:33`).
- **Behaviour:**
  1. **get_cors:** 200 with BC-2's document
     ([rgw_rest_s3.cc:3749-3770](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3749-L3770),
     [rgw_rest_s3.cc:4200-4221](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4200-L4221)).
  2. **put_cors:** the body read up to `rgw_max_put_param_size`; a parse failure or no
     `CORSConfiguration` root is MalformedXML; more rules than `rgw_cors_rules_max_num`
     (default 100, a negative value meaning 100) is 400 InvalidRequest "The number of CORS
     rules should not exceed allowed limit of N rules."; 200 with an empty body
     ([rgw_rest_s3.cc:3772-3841](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3772-L3841),
     [rgw_rest_s3.cc:4223-4292](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4223-L4292);
     [rgw.yaml.in:26-30](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L26-L30), [rgw.yaml.in:26-30](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L26-L30)).
  3. **delete_cors:** 204, with or without a configuration, one that does not decode included,
     which BC-3's op removes
     ([rgw_rest_s3.cc:3843-3852](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3843-L3852), [rgw_rest_s3.cc:4294-4303](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4294-L4303)).
  4. **options_cors**, at bucket and at object scope:
     - 200 with the preflight's Access-Control headers, else the op's 403, 400, 404, 301 or 500
       ([rgw_rest_s3.cc:3854-3875](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3854-L3875),
       [rgw_rest_s3.cc:4305-4326](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4305-L4326)).
     - The headers are rendered once, by BC-7's `SetPreflightHeaders` from `OptionsCORS`'s
       results, which passes `ResponseHeaders` the constant `authorized = true`: radosgw's
       preflight sends the Origin as the request sent it, whatever the rule holds
       ([rgw_rest_s3.cc:3872-3873](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3872-L3873),
       [rgw_rest_s3.cc:4323-4324](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4323-L4324);
       `internal/cors/match.go:232-236`). The route derives no input of the rendering from
       `r.Identity`: telling an anonymous identity from an Unverified one there would make a
       forged preflight's answer differ from an anonymous one's.
     - No preflight's response carries x-amz-request-charged, an anonymous preflight's included,
       on success and on the error writer's 200-399 path, which takes the 301 PermanentRedirect
       of another zonegroup's bucket, whose `r.BucketRec` `OptionsCORS.Init` leaves set
       (`internal/s3/errors.go:50-54`). radosgw sends it to a requester that does not own a
       requester-pays bucket, an anonymous one included
       ([rgw_rest.cc:597-601](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L597-L601),
       [rgw_rest.cc:602-606](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L602-L606)).
       BC-7's rule in `SetCommonHeaders` keeps it off every OPTIONS response, so this task
       touches neither `headers.go` nor `errors.go`.
     - The route calls nothing else that reads the key's user. At object scope it builds the op
       without reading the object, as radosgw's `read_permissions` reads the bucket alone for
       OP_OPTIONS
       ([rgw_rest.cc:1930-1932](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1930-L1932),
       [rgw_rest.cc:1929-1931](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1929-L1931)).
  5. **Storage class:** each of the four routes sets its op's `StorageClass` from
     `x-amz-storage-class`, as PutObject's route does (`internal/s3/putobject.go:58`). BC-3's
     ops make radosgw's refusal of a class the bucket's placement lacks, 400 InvalidArgument
     ([rgw_op.cc:576-583](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L576-L583),
     [rgw_op.cc:606-613](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L606-L613)).
- **Specs:**
  - status, headers and body of each route from test_set_cors, test_cors_header_option,
    test_cors_origin_wildcard and the presigned preflight cases;
  - BC-3's invariant table end to end through the route, its rows the anonymous preflight and a
    forged one with each of BC-3's keys, on a requester-pays bucket the table names `:plain`, so
    that every key, the tenanted one included, resolves it alike. Each gives the same status,
    headers and body, apart from request ids; none carries x-amz-request-charged, the
    anonymous one included, in every entry, the 301 of another zonegroup's bucket among them;
    and none writes usage charged to its key's user. The table gains an object-scope entry,
    of a key that does not exist, answered 200;
  - the tenant ruling end to end, through the real handler and `PostAuthInit`
    (`internal/s3/handler.go:404`): a tenanted key's preflight of `plain` gives what an
    anonymous preflight of `t:plain` gives, and not what one of `plain` gives;
  - a successful preflight on a rule that allows `*`, with and without an Authorization header,
    sends the Origin as sent;
  - each route with an `x-amz-storage-class` the placement lacks is 400 InvalidArgument, the
    three configuration routes' only once the requester is authorized, and the preflight's
    answer is the same for every key.
- **Exclusions and registry:** retires "A CORS preflight is answered 501 until CORS lands" and
  the last paragraph of "A preflight's usage is accounted as an anonymous request's"; puts the
  routed preflight of "A preflight's answer says nothing of its key's user", the delete of
  "DeleteBucketCors removes a CORS configuration that does not decode" and the routed ops of
  "The CORS configuration ops check the storage class once the requester is authorized" in the
  present tense; and does the same in the registry entry "radosgw grants a signed CORS
  preflight without comparing its signature", whose rgw-go paragraph still says the preflight
  is answered 501.
- **Depends on:** S2, BC-3, BC-5 (the presigned preflight specs), BC-7 (the preflight seam).
  **PR:** one. **Brief:** about 170 lines.

### Task BC-7: CORS headers on responses

- **Layer:** handler. **Files:** modify `internal/s3/headers.go` (`SetCommonHeaders`, and a new
  `SetPreflightHeaders`). **Cluster:** no.
- **Consumes:** BC-2's `Decode`, `Match`, `ResponseHeaders`; BC-3's `AttrCORS` and
  `OptionsCORS`. **Produces:** `SetPreflightHeaders(w http.ResponseWriter, o *op.OptionsCORS)`,
  which BC-6's options_cors route calls, and `SetCommonHeaders`' rule for an OPTIONS response.
- **Behaviour** (B1, D15):
  1. When the request carries an Origin and its bucket record is loaded with a configuration
     that decodes, `SetCommonHeaders` adds `ResponseHeaders` of the matched rule, for the
     Access-Control-Request-Method if sent, else the request's method.
  2. That covers every response `SetCommonHeaders` writes, error documents included, which is
     radosgw's `end_header` with the op passed and `abort_early` after the bucket loads
     ([rgw_rest.cc:589-655](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L589-L655), [rgw_rest.cc:594-660](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L594-L660);
     [rgw_rest.cc:676-750](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L676-L750), [rgw_rest.cc:681-750](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L681-L750)).
  3. **Differs from radosgw by design:** radosgw omits the headers on the 15 routes whose
     `end_header` gets no op, ListBuckets, CreateBucket, the four CORS ops,
     PutBucketEncryption, DeleteBucketEncryption, PutPublicAccessBlock and the object-lock and
     request-payment PUTs among them (the research's scan of `end_header` calls, §2.1). rgw-go adds
     them where a bucket is loaded; a request with no bucket record gets none.
  4. An undecodable configuration adds no headers; the op answers it.
  5. **The preflight's response.** Dispatch sends an OPTIONS request to options_cors alone, so
     `SetCommonHeaders` treats every OPTIONS response as the preflight's, on success and on the
     error writer's 200-399 path (`internal/s3/errors.go:50-54`) alike:
     - it adds no Access-Control headers of its own: the preflight's are rendered once, by
       `SetPreflightHeaders`, from `OptionsCORS`'s rule, Origin, method and requested headers,
       with `ResponseHeaders`' `authorized` the constant true, as radosgw's preflight sends the
       Origin as the request sent it
       ([rgw_rest_s3.cc:3872-3873](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3872-L3873),
       [rgw_rest_s3.cc:4323-4324](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4323-L4324)).
       Neither function derives anything for a preflight from `r.Identity` or the
       Authorization header. radosgw's preflight errors carry no Access-Control headers
       (`end_header(s, NULL)`, the same lines' function), and neither do rgw-go's;
     - it sends no x-amz-request-charged, whatever the identity, an anonymous one included: on a
       forged preflight the header would say whether the key's user owns the bucket, and
       sending it to an anonymous preflight alone would tell a forged one from an anonymous one
       (docs/exclusions.md, "A preflight's answer says nothing of its key's user"). radosgw
       sends it to every non-owner
       ([rgw_rest.cc:597-601](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L597-L601),
       [rgw_rest.cc:602-606](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L602-L606)).
- **Specs:** GET object 200 and 404 with the headers; an authentication failure without them;
  a presigned request on a wildcard rule gets `*`; a method the rule does not allow; a
  PutBucketEncryption response with the headers; an OPTIONS response, 200 and 301, anonymous and
  Unverified, on a requester-pays bucket the identity does not own, with no
  x-amz-request-charged and no Access-Control headers from `SetCommonHeaders`;
  `SetPreflightHeaders` on a rule that allows `*` sends the Origin as sent.
- **Exclusions and registry:** new exclusion "Every S3 response carries CORS headers once its
  bucket is loaded", listing radosgw's omissions, and that a preflight's carries only its own.
- **Depends on:** BC-2, BC-3. **PR:** one. **Brief:** about 150 lines.

### Task BC-8: CORS recording and gate

- **Layer:** integration. **Files:** `hack/s3tests` deselect lines 92-108, both ledgers, the
  known-differences files; new `test/integration/phase2_cors_test.go`. **Cluster:** yes.
- **Behaviour:**
  1. Delete the 17 CORS deselect lines and record both ledgers from radosgw
     (`make s3tests-record-tests`) before rgw-go's outcomes are compared.
  2. The source predicts radosgw fails the four `_v2` presigned tests (`fails_on_rgw`),
     test_cors_origin_response and test_cors_presigned_url_non_preflight. The last two become
     known-differences lines under BC-4's exclusion, once the recording confirms them.
  3. Gate case 1, both directions, both releases: radosgw's PutBucketCors read by rgw-go's
     Get and honoured by its preflight and headers; rgw-go's read by radosgw's; the
     `user.rgw.cors` bytes equal for the same document, wildcard, multi-origin, expose-header
     and max-age rules included.
  4. Reproduce defects 1, 2 and 4 on both releases and set their registry status; run each
     entry's prior-art search. Do the same for the two registry entries BC-2 added: the
     wildcard over-match ("radosgw's CORS wildcard match allows origins and headers its
     pattern does not cover": an AllowedOrigin `* example.com` admitting `evilexample.com`,
     and an overlapping pattern), and the CVE-2021-3524 carriage return ("radosgw from Quincy
     on lacks CVE-2021-3524's fix for CORS expose headers": `&#13;` in an ExposeHeader sent
     as a bare carriage return on the wire).
  5. Add s3-tests tests for the first rule that matches origin, method and requested headers,
     merged into jhoblitt/ceph's `rgw-go-s3tests` branch: a later rule for the origin that
     allows PUT where the first allows only GET, and a later rule that allows a requested
     header the first lacks, each by preflight and by an ordinary request's headers. radosgw
     fails them on both floors, so they become known-differences lines under "CORS rules are
     matched as Amazon S3 matches them" once the recording confirms it; the pin bump that
     brings them in is its own change.
- **Specs:** the gate cases above, labelled `integration, rgw-go, phase2, cors`, and the
  s3-tests tests of behaviour 5.
- **Depends on:** BC-3 to BC-7. **PR:** one per release's recording, landed one at a time by
  the controller. **Brief:** about 80 lines.

**Static website: BC-9 to BC-16.** The ceph files `rgw_website.h` and `rgw_website.cc` are
byte-identical at the two tags, and `rgw_rest_s3website.h` differs only by a later
`canonical_name` line, so their citations hold at both.

### Task BC-9: Website configuration type

- **Layer:** meta. **Files:** create `internal/meta/website.go`; modify
  `internal/meta/bucket_info.go`, `bucket_json.go`, `dump_bucket.go`. **Cluster:** no.
- **Consumes:** `internal/denc`. **Produces:** `meta.WebsiteConf` with its sub-structs, and
  `BucketInfo.Website *WebsiteConf` in place of today's opaque `RawStruct`
  (`internal/meta/bucket_info.go:101-102`).
- **Behaviour:**
  1. **Encoding.** RGWBucketWebsiteConf is ENCODE_START(2,1): index_doc_suffix, error_doc,
     routing_rules, redirect_all, then v2's subdir_marker, listing_css_doc, listing_enabled,
     decoded only when struct_v is 2 or more; the sub-structs are each (1,1)
     ([rgw_website.h:26-243](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.h#L26-L243), [rgw_website.h:26-243](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.h#L26-L243)). The
     derived `is_redirect_all` and `is_set_index_doc` are not encoded.
  2. **Rewrite (B17).** A v1 configuration decodes and is written back as v2 on any instance
     rewrite, as radosgw's encoder does. Phase 0's write-back of the v1 bytes unchanged
     (`internal/meta/bucket_info.go:262-264`) goes.
  3. **JSON (B17).** The bucket instance's `website_conf` is radosgw's lossy form: only
     `redirect_all` when its hostname is set, else index_doc_suffix, error_doc and
     routing_rules; decode reads the same keys
     ([rgw_website.cc:135-212](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L135-L212), [rgw_website.cc:135-212](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L135-L212); the
     instance's calls at [rgw_common.cc:2529-2531](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2529-L2531), [rgw_common.cc:2569-2571](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2569-L2571),
     [rgw_common.cc:2591-2593](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2591-L2593), [rgw_common.cc:2628-2630](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2628-L2630)).
  4. The JSON refusal stays for the sync policy only (`internal/meta/bucket_info.go:351-356`,
     `internal/meta/bucket_json.go:98-126`, `internal/meta/dump_bucket.go:46-51`), so the admin
     metadata API stops answering 501 for a bucket with a website (`internal/driver/metadata.go:90`).
- **Specs:** hand-built v1 and v2 encodings of every sub-struct; an RGWBucketInfo carrying a
  website round-trips (BC-16 compares it with a radosgw-written instance through ceph-dencoder);
  the JSON of a redirect-all and of an index-and-rules configuration; a v1 input re-encoded as v2.
- **Exclusions and registry:** retires the website half of "Some bucket sub-records are
  carried opaque in phase 0." and the metadata API's 501 for a website configuration.
- **Depends on:** nothing. **PR:** one. **Brief:** about 200 lines.

### Task BC-10: Website semantics and XML

- **Layer:** library. **Files:** create `internal/website/`. **Cluster:** no.
- **Consumes:** BC-9's `meta.WebsiteConf`; `internal/xmltext`. **Produces:**
  `ParseXML(body []byte, maxRules int) (meta.WebsiteConf, error)`, `MarshalS3XML(conf) []byte`,
  `EffectiveKey`, `Redirect`, `Match` and `Apply` (signatures in Seams).
- **Behaviour:**
  1. **Parsing** ([rgw_rest_s3.cc:2279-2345](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2279-L2345),
     [rgw_rest_s3.cc:2374-2440](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2374-L2440)):
     - an unparseable body or no `WebsiteConfiguration` is 400 MalformedXML (B2), where both
       floors answer InvalidArgument;
     - RedirectAllRequestsTo without HostName, neither RedirectAllRequestsTo nor
       IndexDocument, and an empty Suffix are each 400 InvalidArgument with radosgw's message;
     - more routing rules than `maxRules` (`rgw_website_routing_rules_max_num`, default 50, a
       negative value meaning 50) is 400 InvalidRequest with radosgw's message
       ([rgw.yaml.in:46-50](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L46-L50), [rgw.yaml.in:46-50](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L46-L50)).
  2. **Element rules.** A HttpRedirectCode outside 301-399, a HttpErrorCodeReturnedEquals
     outside 400-599, and ReplaceKeyPrefixWith with ReplaceKeyWith are 400 InvalidArgument
     ([rgw_website.cc:214-341](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L214-L341), [rgw_website.cc:214-341](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L214-L341)).
     A document with RedirectAllRequestsTo and any other property is 400 MalformedXML, as
     AWS's model rules of RedirectAllRequestsTo: "If you specify this property, you can't
     specify any other property" (aws-sdk-go v1.55.8 `service/s3/api.go:43886-43889`);
     radosgw ignores the rest (`RGWBucketWebsiteConf::decode_xml`, [rgw_website.cc:323-341](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L323-L341),
     [rgw_website.cc:323-341](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L323-L341)).
     The rest of the strict reading, AWS's other constraints (an ErrorDocument's Key, a Suffix
     without a slash, a rule's Redirect, a Protocol of http or https) and the empty Condition,
     Redirect and RoutingRules it accepts are the exclusion "PutBucketWebsite answers
     MalformedXML for a document that does not parse or fit S3's schema".
  3. **Rendering.** `WebsiteConfiguration` in the S3 namespace: RedirectAllRequestsTo (HostName,
     then Protocol when set), IndexDocument/Suffix, ErrorDocument/Key, RoutingRules, each only
     when set (same lines).
  4. **EffectiveKey** is `get_effective_key`: no suffix is false; an empty key is the suffix; a
     key ending in `/` is key+suffix; a key that is not a file is key+`/`+suffix; else the key
     ([rgw_website.cc:116-133](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L116-L133),
     [rgw_website.cc:116-133](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L116-L133)).
  5. **Redirect** is `should_redirect` with `apply_rule`: a redirect-all, or the first routing
     rule whose KeyPrefixEquals prefixes the key and whose error condition matches the status
     (0 matching a rule with none). The Location is protocol (the rule's, else the request's)
     `://` host (the rule's, else the Host header) `/` and the replaced key; the code is the
     rule's HttpRedirectCode, else 301
     ([rgw_website.cc:34-114](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L34-L114), [rgw_website.cc:34-114](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L34-L114)).
     The replaced key is percent-encoded as radosgw's `url_encode` encodes a key with the slash
     kept ([rgw_common.cc:1736-1793](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1736-L1793), [rgw_common.cc:1799-1856](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1799-L1856)), where radosgw
     writes it raw, so a key's CR, LF, `?`, `#` and `%` cannot reshape the header or the URL.
     A host holding a byte at or below 0x20, or 0x7F, or a protocol other than http or https,
     which `apply_rule` writes raw too
     ([rgw_website.cc:45-59](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L45-L59), [rgw_website.cc:45-59](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L45-L59)), is `ErrUnsafeRedirect`,
     so a configuration radosgw stored fails closed; the parse refuses such a HostName as
     InvalidArgument.
- **Specs:** a table from each cited function; the redirect-code and error-code bounds; the
  routing-rule limit with its message; the unparseable body's MalformedXML.
- **Exclusions and registry:** new exclusion for the MalformedXML code (B2), one for the
  Location's encoded key and one for the declined host and protocol; new registry entry for
  defect 15 (S3-correctness, minor, unreproduced) and one for the Location's raw key, host and
  protocol.
- **Depends on:** BC-9. **PR:** one. **Brief:** about 200 lines.

### Task BC-11: Website configuration ops

- **Layer:** op. **Files:** create `internal/op/website.go`. **Cluster:** no.
- **Consumes:** BC-9's type; `BucketStore.PutBucketInfo` (`internal/op/bucket.go:114-117`).
  **Produces:** `GetBucketWebsite{Conf}`, `SetBucketWebsite{Params}`, `DeleteBucketWebsite`.
- **Behaviour:**
  1. **Get.** Action s3:GetBucketWebsite with bucket tags; no website is
     ErrNoSuchWebsiteConfiguration
     ([rgw_op.cc:2862-2885](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2862-L2885),
     [rgw_op.cc:3094-3117](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3094-L3117)).
  2. **Set.** Action s3:PutBucketWebsite. It sets `has_website` and the configuration and
     rewrites the instance with `PutBucketInfo` under `RetryRacedBucketWrite`, authorized again
     on each retry (B9; [rgw_op.cc:2887-2936](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2887-L2936),
     [rgw_op.cc:3119-3168](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3119-L3168)).
  3. **Delete.** Action s3:DeleteBucketWebsite; clears both fields and rewrites the instance;
     success with nothing set
     ([rgw_op.cc:2938-2981](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2938-L2981),
     [rgw_op.cc:3170-3213](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3170-L3213)).
- **Specs:** each op's error order with fakes; a lost race whose winner changed the bucket
  keeps the winner's change; a retry re-authorized; a stored v1 configuration read by Get and
  written back as v2 by Set.
- **Exclusions and registry:** extends "A PutBucketAcl that loses a race is retried, and every
  retried ACL, policy or tagging write is authorized again" with the website writes.
- **Depends on:** BC-9. **PR:** one. **Brief:** about 120 lines.

### Task BC-12: Website configuration routes

- **Layer:** handler. **Files:** S2's `internal/s3/website.go`. **Cluster:** no.
- **Consumes:** BC-10's `ParseXML` and `MarshalS3XML`, BC-11's ops.
- **Behaviour:**
  1. With `rgw_enable_static_website` false, its default, the three `?website` routes answer
     405 MethodNotAllowed, as radosgw finds no op for them
     ([rgw.yaml.in:2436-2443](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L2436-L2443), [rgw.yaml.in:2530-2537](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2530-L2537);
     [rgw_rest_s3.cc:4674-4679](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4674-L4679),
     [rgw_rest_s3.cc:5186-5191](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5186-L5191); the PUT and
     DELETE gates at [rgw_rest_s3.cc:4732-4737](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4732-L4737),
     [rgw_rest_s3.cc:5248-5253](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5248-L5253),
     [rgw_rest_s3.cc:4796-4801](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4796-L4801),
     [rgw_rest_s3.cc:5311-5316](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5311-L5316)).
  2. Otherwise: PUT 200; DELETE 204; GET the document
     ([rgw_rest_s3.cc:2347-2383](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2347-L2383), [rgw_rest_s3.cc:2442-2478](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2442-L2478)).
  3. PUT reads the body, under `rgw_max_put_param_size`, only after the request is
     authorized: radosgw reads it in `get_params`, which `RGWSetBucketWebsite::execute` calls
     first ([rgw_op.cc:2905-2910](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2905-L2910), [rgw_op.cc:3137-3142](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3137-L3142)), after
     `verify_permission` ([rgw_process.cc:225-255](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L225-L255), [rgw_process.cc:225-258](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L225-L258)).
     So only an authorized requester reaches the parse and its cost.
- **Specs:** each route's status, headers and body; the 405 with the option off; the routing
  rule limit read from the option.
- **Exclusions and registry:** none.
- **Depends on:** S2, BC-10, BC-11. **PR:** one. **Brief:** about 110 lines.

### Task BC-13: Website requests

- **Layer:** request parsing. **Files:** modify `internal/cephconf/apis.go`, `internal/vhost/`,
  `internal/s3/request.go`, `internal/op/request.go`, `internal/s3/multipart.go`
  (`completeLocation`). **Cluster:** no.
- **Produces:** `cephconf.EnabledAPIs` reporting `s3website` and its priority against `s3`;
  `op.Request.Website bool` and `op.Request.Redirect string`.
- **Behaviour:**
  1. `s3website` in `rgw_enable_apis` enables the website handler, as it enables S3
     ([rgw_appmain.cc:301-312](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L301-L312), [rgw_appmain.cc:310-321](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L310-L321));
     today it is logged as ignored (`internal/cephconf/apis.go:18-41`).
  2. The s3website hostnames are `rgw_dns_s3website_name` and the zonegroup's
     `hostnames_s3website`, empty names dropped ([rgw_rest.cc:235-238](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L235-L238),
     [rgw_rest.cc:235-238](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L235-L238)). A Host in them, directly or through the CNAME
     fallback, is a website request whose domain is the s3website one
     ([rgw_rest.cc:2075-2081](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2075-L2081),
     [rgw_rest.cc:2087-2093](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2087-L2093)).
  3. `s3website` listed before `s3` makes every request with a Host a website request
     ([rgw_rest.cc:2150-2156](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2150-L2156),
     [rgw_rest.cc:2162-2168](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2162-L2168)). **Differs:** v20.2.4 also
     makes a request with no Host one ([rgw_rest.cc:2193-2198](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2193-L2198));
     rgw-go applies that on both releases (B14).
  4. CompleteMultipartUpload's Location takes the s3website domain when the request came in
     on one, as `compute_domain_uri` reads the domain preprocess set
     ([rgw_rest.h:805-818](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.h#L805-L818), [rgw_rest.h:817-830](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.h#L817-L830)).
- **Specs:** each Host form (a website host, a bucket subdomain of one, a CNAME to one, a plain
  S3 host, no Host) against each `rgw_enable_apis` order, on both releases.
- **Exclusions and registry:** updates the website halves of "The CNAME fallback resolves no
  CNAME and knows no s3website hostnames." and "CompleteMultipartUpload's Location finds its
  domain without CNAME lookups or website hostnames."; new exclusion for the no-Host rule on
  Squid (B14).
- **Depends on:** nothing. **PR split:** (a) `cephconf` and `vhost`; (b) the request flag and
  the Location. **Brief:** about 200 lines.

### Task BC-14: Website retarget

- **Layer:** op. **Files:** create `internal/op/website_get.go`; modify `internal/op/getobject.go`
  (one hook field, one call). **Cluster:** no.
- **Consumes:** BC-10's `EffectiveKey`, `website.Match` and `website.Apply`; BC-13's
  `Request.Website`. **Produces:** the retarget hook GetObject calls between its bucket load and
  its object read.
- **Behaviour** (`retarget`, run after the bucket loads and before read permissions,
  [rgw_process.cc:180-190](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L180-L190), [rgw_process.cc:180-190](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L180-L190)):
  1. No bucket is NoSuchBucket; no website is NoSuchWebsiteConfiguration; no index suffix is
     400 InvalidArgument "The IndexDocument Suffix is not configurated or not well formed!"
     ([rgw_rest_s3.cc:5387-5448](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5387-L5448),
     [rgw_rest_s3.cc:5913-5974](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5913-L5974)).
  2. `isFile` is a stat of the key less one trailing `/`
     ([rgw_rest_s3.cc:5346-5370](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5346-L5370),
     [rgw_rest_s3.cc:5876-5896](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5876-L5896); the tags
     differ only in how the stat loads state).
  3. A redirect is `ErrWebsiteRedirect` with `r.Redirect` and the rule's code; otherwise the
     effective key is read as a GetObject. `retarget` matches the rule on the effective key,
     passing `new_obj.name` to `should_redirect`, and builds the Location from the request's
     original key, passing `key_name` to `apply_rule`
     ([rgw_rest_s3.cc:5419-5429](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5419-L5429),
     [rgw_rest_s3.cc:5945-5955](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5945-L5955)). BC-14 does the same with
     `Match` and `Apply`, and returns `Apply`'s `ErrUnsafeRedirect` for BC-15 to answer.
  4. An object holding `user.rgw.x-amz-website-redirect-location` is 301 with that Location and
     no body ([rgw_rest_s3.cc:274-292](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L274-L292),
     [rgw_rest_s3.cc:271-289](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L271-L289)).
  5. **The empty host.** radosgw takes the host from `HTTP_HOST`, empty when the request has
     none ([rgw_rest_s3.cc:5424](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5424), [rgw_rest_s3.cc:5950](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5950)), so a request
     without a Host, a website request under B14, redirects to `http:///` or `https:///` and
     the key. A URL
     parser that follows the WHATWG standard skips those slashes, so a key `evil.com/x` sends
     the client to `evil.com`; only the requester meets it, since browsers always send a Host
     (the registry's "radosgw writes a website redirect's key, host and protocol raw into its
     Location"). BC-14 decides between radosgw's form, no redirect without a host, and the
     configured website domain.
- **Specs:** each effective-key case; a directory object; a redirect-all; a routing rule with
  and without a code; an object with a redirect attr; the original key kept for BC-15; the
  empty-host choice.
- **Exclusions and registry:** a new exclusion if the empty-host choice departs from radosgw.
- **Depends on:** BC-10, BC-13. **PR:** one. **Brief:** about 180 lines.

### Task BC-15: Website serving and errors

- **Layer:** handler. **Files:** create `internal/s3/website_serve.go`, `internal/s3/html.go`;
  modify `internal/s3/dispatch.go` (one call line), `handler.go`, `errors.go`. **Cluster:** no.
- **Consumes:** BC-12, BC-14. **Produces:** the website dispatch and the HTML error renderer.
- **Behaviour:**
  1. **Dispatch.** A website request has a GET and a HEAD op at every scope, both the website
     GetObject; every other method is 405
     ([rgw_rest_s3website.h:20-72](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3website.h#L20-L72), [rgw_rest_s3website.h:20-72](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3website.h#L20-L72);
     the handler choice [rgw_rest_s3.cc:5285-5307](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5285-L5307),
     [rgw_rest_s3.cc:5815-5874](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5815-L5874)).
  2. **HTML errors.** Every website-mode refusal is rendered as ceph's HTMLFormatter writes it:
     `<html><head><title>STATUS NAME</title></head><body><h1>STATUS NAME</h1><ul>`, one
     `<li>Name: value</li>` per field ([rgw_rest.cc:1808](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1808), [rgw_rest.cc:1807](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1807);
     [HTMLFormatter.cc:69-91](https://github.com/ceph/ceph/blob/v19.2.7/src/common/HTMLFormatter.cc#L69-L91),
     [HTMLFormatter.cc:69-91](https://github.com/ceph/ceph/blob/v20.2.4/src/common/HTMLFormatter.cc#L69-L91)).
  3. **Error handler.** A routing rule matching the original key and the error's HTTP status
     redirects; else the error document, when configured, is served by a nested GetObject with
     its conditionals cleared and the original status; else, or when that fails, the HTML page
     ([rgw_rest_s3.cc:5460-5585](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5460-L5585),
     [rgw_rest_s3.cc:5986-6111](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5986-L6111)).
  4. WebsiteRedirect carries Location. BC-10's `website.Apply` percent-encodes its key and
     declines a host or protocol that would put a control byte in it, so net/http's folding of
     CR and LF in a header value never meets a Location, and no exclusion is needed for it.
  5. **A redirect code of 0, 100-199, 200, or outside 100-999 is answered 301.**
     `website.Apply` returns the code a rule stores, any `uint16` above 0 (it answers a stored 0
     as 301 already), and only the admin JSON path can store one outside 301-399
     (`RGWRedirectInfo::decode_json`,
     [rgw_website.cc:142-148](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L142-L148), [rgw_website.cc:142-148](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L142-L148)). BC-15 maps 0 and
     200 to WebsiteRedirect's 301, as radosgw's `abort_early` calls `set_req_state_err` over an
     `http_ret` of 0 or 200
     ([rgw_rest.cc:702-706](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L702-L706), [rgw_rest.cc:707-711](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L707-L711);
     `ERR_WEBSITE_REDIRECT`, [rgw_common.cc:58](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L58), [rgw_common.cc:59](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L59)). It maps a code
     in 100-199, 101 included, or outside 100-999 to 301 too, where radosgw writes the code as
     given in its status line (`ClientIO::send_status`,
     [rgw_asio_client.cc:109-118](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_client.cc#L109-L118), [rgw_asio_client.cc:109-118](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_client.cc#L109-L118)). net/http's
     `WriteHeader` panics outside 100-999 (`checkWriteHeaderCode`, `net/http/server.go:1166-1180`
     at Go 1.27.1); it writes 100-199 other than 101 as an informational response that does not
     end the response, and 101 as a final one that announces a protocol switch
     (`net/http/server.go:1223-1227` at Go 1.27.1). Neither gives the redirect radosgw's status
     line does.
  6. **An unsafe redirect is 500 InternalError.** `website.ErrUnsafeRedirect`, from BC-14's hook
     or the error handler's `website.Redirect`, is answered 500 InternalError, so a stored host
     holding a control byte, or a protocol other than http or https, fails closed where radosgw
     writes it raw (BC-10's exclusion "A website redirect is refused for a host holding a
     control byte or a protocol other than http or https").
- **Specs:** status, headers and body for each path; an error document that is itself missing
  falls back to the HTML page; a conditional GET's error document served unconditionally; PUT
  on a website host 405 in HTML; a rule storing code 200, 100, 101, 99 or 1000 answered 301;
  a stored HostName holding CR and LF answered 500 on both paths.
- **Exclusions and registry:** a new exclusion line: a stored redirect code in 100-199, or
  outside 100-999, is answered 301, where radosgw writes it as given.
- **Depends on:** S2, BC-12, BC-14. **PR split:** (a) the HTML renderer and website-mode
  refusals; (b) serving and the error handler. **Brief:** about 230 lines.

### Task BC-16: Website gate

- **Layer:** integration. **Files:** new `test/integration/phase2_website_test.go`.
  **Cluster:** yes.
- **Behaviour:** gate case 2, both directions, both releases: radosgw's PutBucketWebsite
  (index, error document, routing rules, redirect-all) served by rgw-go's endpoint, and
  rgw-go's by radosgw's; the bucket instance equal through ceph-dencoder's RGWBucketInfo dump;
  a hand-written v1 configuration rewritten by each comes out v2; `radosgw-admin metadata get
  bucket.instance` equal to rgw-go's admin get. It also captures the RGWBucketInfo vectors BC-9's
  specs then pin. No s3-test at the pin carries the website markers.
- **Specs:** the gate cases above, labelled `integration, rgw-go, phase2, website`.
- **Depends on:** BC-9 to BC-15. **PR:** one. **Brief:** about 70 lines.

**Public access block and policy status: BC-17 to BC-22.** Enforcement of a stored block is
phase 1's and unchanged: `authz.publicAccess` decodes the attr and refuses an undecodable one
(`internal/authz/load.go:70-92`), the evaluator carries it with the bucket inputs
(`internal/authz/evaluator.go:249-345`), and the write refusals are in
`internal/authz/write.go:112-159` and `:255-267`. The block ops plug in only by writing and
removing `user.rgw.public-access`. `rgw_public_access.cc` is byte-identical at the two tags.

### Task BC-17: Block XML

- **Layer:** library. **Files:** modify `internal/acl/publicaccess.go`. **Cluster:** no.
- **Consumes:** `acl.PublicAccessBlock` with its v1 Encode and Decode
  (`internal/acl/publicaccess.go:9-38`). **Produces:** `ParsePublicAccessBlockXML(body []byte)
  (PublicAccessBlock, error)` and `(PublicAccessBlock).MarshalXML() []byte`.
- **Behaviour:**
  1. A parse failure or no `PublicAccessBlockConfiguration` root is 400 MalformedXML
     ([rgw_op.cc:8633-8675](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8633-L8675),
     [rgw_op.cc:9601-9643](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9601-L9643)).
  2. Each of the four booleans is optional; the document renders
     `PublicAccessBlockConfiguration` without the S3 namespace, each boolean `true` or `false`
     (B2; [rgw_public_access.cc:4-20](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_public_access.cc#L4-L20), [rgw_public_access.cc:4-20](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_public_access.cc#L4-L20)).
- **Specs:** each boolean present and absent; a non-boolean value; the rendered bytes.
- **Exclusions and registry:** new registry entry for defect 16 (cosmetic, from the source;
  rgw-go keeps radosgw's form under D16), covering BC-21's booleans too.
- **Depends on:** nothing. **PR:** one. **Brief:** about 70 lines.

### Task BC-18: Block ops

- **Layer:** op. **Files:** create `internal/op/publicaccess.go`; modify `internal/op/authz.go`
  (`Authorizer`) and `internal/authz/evaluator.go` for behaviour 5's check, a bucket check
  without the ACL fallback, which neither offers today. **Cluster:** no.
- **Consumes:** BC-17; `op.AttrPublicAccess` (`internal/op/putobject.go:35-37`). **Produces:**
  `GetPublicAccessBlock{Block}`, `PutPublicAccessBlock{Params}`, `DeletePublicAccessBlock`.
- **Behaviour:**
  1. **Put.** Action s3:PutBucketPublicAccessBlock with bucket tags; it writes only
     `user.rgw.public-access` on each try (B9), where radosgw merges the attrs the request
     started with ([rgw_op.cc:8613-8624](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8613-L8624),
     [rgw_op.cc:9581-9592](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9581-L9592);
     [rgw_op.cc:8669-8673](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8669-L8673),
     [rgw_op.cc:9637-9641](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9637-L9641)).
  2. **Get.** Action s3:GetBucketPublicAccessBlock; no attr is ErrNoSuchPublicAccessBlock with
     "The public access block configuration was not found"; an undecodable one is 500
     ([rgw_op.cc:8677-8712](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8677-L8712),
     [rgw_op.cc:9645-9680](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9645-L9680)).
  3. **Delete.** Authorized as s3:PutBucketPublicAccessBlock; removes the attr; success when
     absent ([rgw_op.cc:8715-8755](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8715-L8755),
     [rgw_op.cc:9683-9723](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9683-L9723)).
  4. A block the op writes reaches every check through the cache-notified bucket record, with
     no change to the checks that enforce it.
  5. **No ACL grant authorizes the block's ops.** Put, Get and Delete are authorized by the
     bucket owner and by identity or bucket policy only: where no policy decides, the owner
     passes and everyone else is refused, and the evaluator's ACL fallback is not consulted.
     - **Reason:** radosgw's `op_to_perm` maps s3PutBucketPublicAccessBlock to RGW_PERM_WRITE_ACP
       ([rgw_iam_policy.h:310-311](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L310-L311),
       [rgw_iam_policy.h:327-328](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L327-L328))
       and s3GetBucketPublicAccessBlock to RGW_PERM_READ_ACP
       ([rgw_iam_policy.h:288-289](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L288-L289),
       [rgw_iam_policy.h:304-305](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L304-L305)).
       Put and Delete's `verify_permission` pass s3PutBucketPublicAccessBlock to
       `verify_bucket_permission`
       ([rgw_op.cc:8619](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8619),
       [rgw_op.cc:8733](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8733);
       [rgw_op.cc:9587](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9587),
       [rgw_op.cc:9701](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9701)), and
       Get's passes s3GetBucketPublicAccessBlock
       ([rgw_op.cc:8683](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8683),
       [rgw_op.cc:9651](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9651)).
       Where no policy decides, `verify_bucket_permission` falls back to the bucket ACL with
       `op_to_perm`'s permission
       ([rgw_common.cc:1370-1371](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1370-L1371),
       [rgw_common.cc:1392-1393](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1392-L1393)).
     - **Consequence in radosgw:** any WRITE_ACP grantee can change or remove the block, and
       any READ_ACP grantee can read it. That includes a named user, AllUsers while
       IgnorePublicAcls is off
       ([rgw_acl.cc:180-187](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L180-L187),
       [rgw_acl.cc:180-187](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L180-L187)),
       and the user `anonymous` through a Swift write list under all four settings: the list
       grants WRITE_OBJS
       ([rgw_acl_swift.cc:22](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl_swift.cc#L22),
       [rgw_acl_swift.cc:22](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl_swift.cc#L22)),
       which the ACL check widens to WRITE and WRITE_ACP
       ([rgw_acl.cc:216-218](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L216-L218),
       [rgw_acl.cc:216-218](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L216-L218)).
     - **AWS:** WRITE_ACP on a bucket corresponds only to s3:PutBucketAcl, and READ_ACP only to
       s3:GetBucketAcl
       (https://docs.aws.amazon.com/AmazonS3/latest/userguide/acl-overview.html#acl-access-policy-permission-mapping).
- **Specs:**
  - each op's error order with fakes; a lost race keeps the winner's ACL; a retry
    re-authorized;
  - a block written here, then enforced by phase 1's checks: BlockPublicAcls on PutBucketAcl,
    IgnorePublicAcls on an anonymous GET, and BlockPublicPolicy and RestrictPublicBuckets
    judged by S3's `IsPublic`;
  - for each op, these are refused with 403 AccessDenied, while the owner and a policy Allow
    succeed: a WRITE_ACP, a FULL_CONTROL and a READ_ACP named grantee; an AllUsers WRITE_ACP
    grant with IgnorePublicAcls off; a Swift `X-Container-Write: anonymous` container.
- **Exclusions and registry:** extends defect 4's exclusion and registry entry with
  PutPublicAccessBlock; extends defect 5's exclusion and "A retried bucket write stops when the
  name names another bucket" with the block writes, citing their retries
  ([rgw_op.cc:8669-8673](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8669-L8673),
  [rgw_op.cc:8749-8754](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8749-L8754),
  [rgw_op.cc:9637-9641](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9637-L9641),
  [rgw_op.cc:9717-9722](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9717-L9722)); adds
  rgw-go's Get answer to the registry entry for defect 7; new exclusion "No ACL grant authorizes the
  public access block's ops"; writes the rgw-go line of the registry entry "radosgw lets an ACL
  grant change a bucket's public access block", which S5's PR adds.
- **Depends on:** BC-17, S3 (the enforcement specs), S5 (that registry entry). **PR:** one.
  **Brief:** about 120 lines.

### Task BC-19: Block routes

- **Layer:** handler. **Files:** S2's `internal/s3/publicaccess.go`. **Cluster:** no.
- **Consumes:** BC-17, BC-18.
- **Behaviour:**
  1. PUT 200 with an empty body; GET the document; DELETE 204
     ([rgw_rest_s3.cc:4604-4624](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4604-L4624),
     [rgw_rest_s3.cc:5115-5135](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5115-L5135)).
  2. **Differs from radosgw by design (B10):** a GET on a bucket without a block sends the 404
     error document alone. radosgw writes the document whatever `op_ret` is, so a default block
     follows the error document past its Content-Length
     ([rgw_rest_s3.cc:4613-4624](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4613-L4624),
     [rgw_rest_s3.cc:5124-5135](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5124-L5135);
     [rgw_rest.cc:620-625](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L620-L625), [rgw_rest.cc:625-630](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L625-L630)),
     and no IO filter truncates it ([rgw_client_io_filters.h:327-365](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_client_io_filters.h#L327-L365),
     [rgw_client_io_filters.h:327-365](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_client_io_filters.h#L327-L365)).
- **Specs:** each status, header and body; the 404's bytes end at its Content-Length.
- **Exclusions and registry:** new exclusion for the 404 (defect 6); new registry entry for
  defect 6 (S3-correctness: it can desynchronize a keep-alive connection; unreproduced).
- **Depends on:** S2, BC-18. **PR:** one. **Brief:** about 90 lines.

### Task BC-20: Policy status op

- **Layer:** op. **Files:** create `internal/op/policystatus.go`. **Cluster:** no.
- **Consumes:** S3's `func (p *Policy) IsPublic() bool`; `acl.Policy.IsPublic`
  (`internal/acl/eval.go:68`). **Produces:** `GetBucketPolicyStatus{IsPublic bool}`.
- **Behaviour:**
  1. Action s3:GetBucketPolicyStatus with bucket tags. IsPublic is the bucket policy's
     `IsPublic` or the bucket ACL's, ignoring the block
     ([rgw_op.cc:8595-8611](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8595-L8611),
     [rgw_op.cc:9563-9579](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9563-L9579)).
  2. **Differs:** the floors' `is_public` differ from each other and from AWS (defects 13 and
     14); rgw-go uses S3's one judgment on both releases (B3).
  3. A stored policy that does not parse is refused by the authorizer before this op runs.
- **Specs:** the bucket shape of each of the six policy-status s3-tests; a public policy under
  a block still reports true.
- **Exclusions and registry:** adds GetBucketPolicyStatus to S3's exclusion and to the registry
  entries for defects 13 and 14 as a consumer of S3's choice.
- **Depends on:** S3. **PR:** one. **Brief:** about 70 lines.

### Task BC-21: Policy status route

- **Layer:** handler. **Files:** S2's `internal/s3/policystatus.go`. **Cluster:** no.
- **Consumes:** BC-20's op.
- **Behaviour:** 200 with `PolicyStatus` in the S3 namespace and IsPublic as lower-case `true`
  or `false` (B2; [rgw_rest_s3.cc:4584-4602](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4584-L4602),
  [rgw_rest_s3.cc:5095-5113](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5095-L5113)).
- **Specs:** both values' bytes; an error document.
- **Exclusions and registry:** none (BC-17's entry covers the booleans).
- **Depends on:** S2, BC-20. **PR:** one. **Brief:** about 60 lines.

### Task BC-22: Block and policy-status recording and gate

- **Layer:** integration. **Files:** `hack/s3tests`' public access block and policy-status
  deselect lines, both ledgers, the known-differences files; new
  `test/integration/phase2_publicaccess_test.go`. **Cluster:** yes.
- **Behaviour:**
  1. Delete the public access block and policy-status deselect lines, phase 1's and those the
     next s3-tests pin bump adds, and record both ledgers from radosgw before comparing. Add
     the known-differences lines that `00-index.md` lists for BC-22 under Global Constraints,
     "The next s3-tests pin bump", each under the exclusion it names.
     - The public access block lines re-select the 18 tests under `# public access block` in
       `hack/s3tests/deselect-phase2.txt`, which call Put, Get and DeletePublicAccessBlock alone,
       directly or through their helpers, and never GetBucketPolicyStatus (s3-tests at the pin
       7cfa64a5, BC-19's report): `test_get_undefined_public_block`,
       `test_get_public_block_deny_bucket_policy`, `test_put_public_block`,
       `test_block_public_put_bucket_acls`, `test_block_public_object_canned_acls`,
       `test_block_public_policy`, `test_block_public_policy_with_principal`,
       `test_block_public_condition_on_unlisted_key`, `test_block_public_anonymous_root_principal`,
       `test_restrict_public_anonymous_root_principal`, `test_block_public_restrict_public_buckets`,
       `test_ignore_public_acls`, `test_anon_canned_write_ignore_public_acls`,
       `test_anon_owned_object_acl_ignore_public_acls`, `test_anon_owned_object_acl_block_public_acls`,
       `test_anon_put_object_block_public_acls`, `test_put_get_delete_public_block` and
       `test_public_block_misspelled_setting_refused`.
  2. Gate case 3: each gateway's PutPublicAccessBlock read back and enforced by the other
     (BlockPublicAcls on PutBucketAcl, IgnorePublicAcls on an anonymous GET, BlockPublicPolicy
     on PutBucketPolicy, RestrictPublicBuckets on Tentacle); the attr bytes equal.
  3. Gate case 5: the same IsPublic from both for public-read ACLs, a public policy and a
     private bucket, with a known-differences line wherever S3's judgment departs from a floor's.
  4. Reproduce defect 6 from the raw response bytes on both releases.
     - After the 404, send a second request on the same connection. Record whether radosgw
       closes the connection after the 404, by a `Connection: close` header or by closing the
       socket, or the surplus block arrives ahead of the second response.
     - The result settles defect 6's security rating. A connection left open keeps the entry
       "possibly security-relevant": a proxy that pools upstream connections can then hand the
       surplus bytes to another client. A connection radosgw closes leaves it S3-correctness
       alone. Update the registry entry, "radosgw sends a public access block after
       GetPublicAccessBlock's error document, past its Content-Length", with the bytes recorded
       and the rating that follows.
- **Specs:** the gate cases above, labelled `integration, rgw-go, phase2, publicaccess`.
- **Depends on:** BC-17 to BC-21. **PR:** one, landed by the controller. **Brief:** about 70
  lines.

**Encryption configuration: BC-23 to BC-27.** "As an unconfigured radosgw" means the three
configuration ops, which need no key server, plus the answer such a radosgw gives a write the
default asks to encrypt. `rgw_bucket_encryption.h` and `rgw_bucket_encryption.cc` are
byte-identical at the two tags.

### Task BC-23: Encryption configuration type

- **Layer:** library. **Files:** create `internal/sse/` (phase 4's SSE-C joins it).
  **Cluster:** no.
- **Produces:** `BucketEncryptionConfig`, `ServerSideEncryptionConfiguration`,
  `ApplyServerSideEncryptionByDefault`, each with Encode and Decode; `ParseXML(body []byte)
  (BucketEncryptionConfig, error)`; `MarshalS3XML`; `Validate`.
- **Behaviour:**
  1. **Encoding.** RGWBucketEncryptionConfig (1,1){rule_exist, rule when set};
     ServerSideEncryptionConfiguration (1,1){apply, bucketKeyEnabled};
     ApplyServerSideEncryptionByDefault (1,1){kmsMasterKeyID, sseAlgorithm}
     ([rgw_bucket_encryption.h:9-141](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.h#L9-L141), [rgw_bucket_encryption.h:9-141](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_bucket_encryption.h#L9-L141)).
  2. **XML.** A parse failure or no `ServerSideEncryptionConfiguration` root is 400
     MalformedXML. Rule, SSEAlgorithm and BucketKeyEnabled are optional on input; the document
     renders SSEAlgorithm, KMSMasterKeyID only when non-empty, and BucketKeyEnabled only when
     true ([rgw_bucket_encryption.cc:8-40](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.cc#L8-L40), [rgw_bucket_encryption.cc:8-40](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_bucket_encryption.cc#L8-L40)).
  3. **Validation (B5, D19).** `Validate` refuses, as 400 MalformedXML, an SSEAlgorithm outside
     AWS's list and a KMSMasterKeyID with an algorithm other than `aws:kms` or `aws:kms:dsse`
     (https://docs.aws.amazon.com/AmazonS3/latest/API/API_ServerSideEncryptionByDefault.html).
     The floors validate neither.
- **Specs:** the ceph-object-corpus goldens of the three structs (18.2.0 and
  19.2.0-404-g78ddc7f9027) decode to ceph-dencoder's JSON and re-encode byte-identically; XML
  both ways; each validation refusal. Its s3-tests additions are BC-25's, where the routes land.
- **Exclusions and registry:** rewrites item 2 of "Key management: SSE-KMS and SSE-S3 with
  every backend", which says the configuration round-trips; new registry entry for defect 12
  (S3-correctness, unreproduced).
- **Depends on:** nothing. **PR:** one. **Brief:** about 150 lines.

### Task BC-24: Encryption configuration ops

- **Layer:** op. **Files:** create `internal/op/bucketencryption.go`. **Cluster:** no.
- **Consumes:** BC-23; `op.AttrBucketEncryption` (`internal/op/putobject.go:17-19`).
  **Produces:** `GetBucketEncryption{Config}`; `PutBucketEncryption`, which reads its own
  body, as `DeleteObjects` does, and takes no handler hook; `DeleteBucketEncryption`; and the
  key-id attr name `user.rgw.sse-s3.key-id`.
- **Behaviour:**
  1. **Put.** Action s3:PutBucketEncryption with no tag loading; it stores the validated
     configuration in `user.rgw.sse-s3.policy` alone on each try
     ([rgw_op.cc:8757-8812](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8757-L8812),
     [rgw_op.cc:9725-9780](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9725-L9780)).
     - After authorization and before `sse.ParseXML`, the body is read as `execute` reads it
       through `get_params`, with `rgw_max_put_param_size` as the limit
       ([rgw_op.cc:8757-8762](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8757-L8762),
       [rgw_op.cc:9725-9730](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9725-L9730);
       [rgw_op.cc:8780](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8780),
       [rgw_op.cc:9748](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9748)). A
       declared length over that `max_len` is 416 InvalidRange (`ERANGE`), unread
       ([rgw_rest.cc:1556-1557](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1556-L1557),
       [rgw_rest.cc:1556-1557](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1556-L1557);
       [rgw_common.cc:128](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L128),
       [rgw_common.cc:129](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L129)). With no
       Content-Length the body is read as chunked, because `read_all_input` drops the
       `allow_chunked=false` that `get_params` passes
       ([rgw_op.h:216-228](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L216-L228),
       [rgw_op.h:229-241](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L229-L241)), and a body that
       is not chunked either is 411 MissingContentLength (`ERR_LENGTH_REQUIRED`)
       ([rgw_rest.cc:1571-1574](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1571-L1574),
       [rgw_rest.cc:1571-1574](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1571-L1574);
       [rgw_common.cc:87](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L87),
       [rgw_common.cc:88](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L88)).
       `sse.MaxConfigXML` is a backstop only.
     - `sse.ErrMalformedXML` from `sse.ParseXML`, which already runs `Validate`, is 400
       MalformedXML.
  2. **Get.** Action s3:GetBucketEncryption; no attr is ErrNoSuchBucketEncryption with "The
     server side encryption configuration was not found"; an undecodable one is 500
     ([rgw_op.cc:8814-8841](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8814-L8841),
     [rgw_op.cc:9782-9809](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9782-L9809)). The tags differ
     only in the log level.
     The attr is read with `sse.DecodeBucketEncryptionConfig(denc.NewDecoder(b))`, any bytes
     after it ignored.
  3. **Delete.** Authorized as s3:PutBucketEncryption; it removes `user.rgw.sse-s3.policy` and
     `user.rgw.sse-s3.key-id`, which only a radosgw with an SSE-S3 backend writes; success when
     absent ([rgw_op.cc:8843-8867](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8843-L8867),
     [rgw_op.cc:9811-9835](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9811-L9835)).
  4. **No op decodes and re-encodes the attr in place.** ceph main 96ed8b8c780, in no release,
     writes the rule at (2,1) with `BlockedEncryptionTypes` after the bucket key
     ([rgw_bucket_encryption.h:91-107](https://github.com/ceph/ceph/blob/96ed8b8c780/src/rgw/rgw_bucket_encryption.h#L91-L107)),
     which BC-23's (1,1) re-encode drops. Put replaces the attr whole and Delete removes it.
- **Specs:** each op's error order with fakes; 411 and 416 before the parse; Delete removing
  both attrs; a retry re-authorized; a stored (2,1) rule read by Get and left as it is.
- **Exclusions and registry:** extends defect 5's exclusion and "A retried bucket write stops
  when the name names another bucket" with the encryption writes, citing their retries
  ([rgw_op.cc:8807-8811](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8807-L8811),
  [rgw_op.cc:8860-8866](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8860-L8866),
  [rgw_op.cc:9775-9779](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9775-L9779),
  [rgw_op.cc:9828-9834](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9828-L9834)).
- **Depends on:** BC-23. **PR:** one. **Brief:** about 130 lines.

### Task BC-25: Encryption configuration routes

- **Layer:** handler. **Files:** S2's `internal/s3/encryption.go`; three s3-tests on jhoblitt/ceph's
  `rgw-go-s3tests`. **Cluster:** no.
- **Consumes:** BC-23's XML, BC-24's ops.
- **Behaviour:**
  1. PUT 200 with an empty body; GET the document in the S3 namespace; DELETE 204
     ([rgw_rest_s3.cc:3877-3915](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3877-L3915),
     [rgw_rest_s3.cc:4328-4366](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4328-L4366)).
  2. GET writes the XML declaration, as `dump_start` does through XMLFormatter's
     `output_header`
     ([rgw_rest_s3.cc:3897](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3897),
     [rgw_rest_s3.cc:4348](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4348);
     [Formatter.cc:426-430](https://github.com/ceph/ceph/blob/v19.2.7/src/common/Formatter.cc#L426-L430),
     [Formatter.cc:434-438](https://github.com/ceph/ceph/blob/v20.2.4/src/common/Formatter.cc#L434-L438)),
     then `MarshalS3XML()`.
  3. **BC-23's s3-tests**, deferred to here, where the routes land, on `rgw-go-s3tests`:
     - (a) an SSEAlgorithm of `AES-256` is 400 MalformedXML;
     - (b) `aws:kms` with a key id and BucketKeyEnabled true reads back through boto3 with
       BucketKeyEnabled True;
     - (c) a misspelled `SseAlgorithm` is 400 MalformedXML.

     Both floors fail all three: they answer 200 to (a) and (c), and boto3 reads (b) as False
     (registry: "radosgw's PutBucketEncryption stores any SSEAlgorithm, and a KMS key id with
     any algorithm", "radosgw's PutBucketEncryption stores a configuration without the elements
     it cannot find", "radosgw writes GetBucketEncryption's BucketKeyEnabled as True, which
     botocore reads as false").
- **Specs:** each status, header and body, from the six configuration s3-tests and the three
  above.
- **Exclusions and registry:** none.
- **Depends on:** S2, BC-24. **PR:** one, with the s3-tests commit. **Brief:** about 100 lines.

### Task BC-26: The default on writes

- **Layer:** op, with one function in authz and the s3 handlers' asked-encryption maps.
  **Files:** create `internal/op/encryption.go`; modify `internal/op/request.go` (`CryptAttrs`),
  `internal/authz/env.go` (`cryptKeys`, `internal/authz/env.go:339`), the op call sites of
  `bucketEncryptionUnserved` (`internal/op/putobject.go:150`, `internal/op/copyobject.go:206`,
  `internal/op/uploadpart.go:214`, `internal/op/initmultipart.go:84`) and `refuseEncryption`
  (`internal/s3/putobject.go:144-171`, called at `internal/s3/putobject.go:87`,
  `internal/s3/multipart.go:101`, `internal/s3/multipart.go:194`, `internal/s3/copyobject.go:102`).
  **Cluster:** no.
- **Consumes:** BC-23. **Produces:** `EncryptionDefaults(r *Request, asked map[string]string)
  (Encryption, error)`, the effective algorithm, key id, bucket-key flag and context;
  `Request.CryptAttrs`, which `cryptKeys` reads in place of the headers when set.
- **Behaviour:**
  1. **`get_encryption_defaults`** ([rgw_rest_s3.cc:154-272](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L154-L272),
     [rgw_rest_s3.cc:151-269](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L151-L269)), called by PutObject,
     POST object and CreateMultipartUpload ([rgw_rest_s3.cc:2620](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2620),
     [rgw_rest_s3.cc:2773](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2773),
     [rgw_rest_s3.cc:2981](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2981),
     [rgw_rest_s3.cc:3118](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3118),
     [rgw_rest_s3.cc:4003](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4003),
     [rgw_rest_s3.cc:4454](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4454)),
     and by UploadPart, which PutObject's `get_params` also serves
     (`RGWHandler_REST_Obj_S3::op_put`,
     [rgw_rest_s3.cc:4875-4878](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4875-L4878),
     [rgw_rest_s3.cc:5402-5405](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5402-L5405)):
     - SSE-C with SSE-KMS or SSE-S3 headers is 400 InvalidArgument, before authorization;
     - SSE-C wins; otherwise the stored algorithm and key id fill what the request names none
       of, KMS attributes without an algorithm mean `aws:kms`, and KMS attributes with another
       algorithm are 400;
     - a stored rule with no algorithm is `aws:kms` when it holds a key id or BucketKeyEnabled
       true, which count as KMS attributes
       ([rgw_rest_s3.cc:226-261](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L226-L261),
       [rgw_rest_s3.cc:223-258](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L223-L258)).
       Both gateways store the BucketKeyEnabled form; radosgw stores the key-id form, which
       BC-23's `Validate` refuses;
     - the filled attributes become `Request.CryptAttrs`, so the policy environment sees the
       default before authorization;
     - a stored configuration that does not decode is 400 InvalidArgument "Server side error -
       can't decode bucket_encryption_conf", after authorization (B16), with the policy
       environment built from the request alone.
  2. **The unconfigured `rgw_s3_prepare_encrypt`** ([rgw_crypt.cc:1030-1301](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_crypt.cc#L1030-L1301),
     [rgw_crypt.cc:1045-1315](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1045-L1315)), after authorization:
     - with `rgw_crypt_require_ssl` (default true) over plain HTTP, 400 InvalidRequest
       ([rgw.yaml.in:2823-2827](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L2823-L2827), [rgw.yaml.in:2955-2959](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2955-L2959)).
       The floors differ: v19.2.7 makes the check for every algorithm, before the algorithm
       check ([rgw_crypt.cc:1152-1156](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_crypt.cc#L1152-L1156)),
       and v20.2.4 only for `aws:kms`
       ([rgw_crypt.cc:1167-1172](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1167-L1172));
     - `aws:kms` without a key id, and an algorithm other than `aws:kms` and `AES256`, 400
       InvalidArgument;
     - otherwise the answer BC-27 records from a running radosgw, which source reading puts at
       400 InvalidArgument (the empty Barbican and Vault addresses,
       [rgw_kms.cc:919-979](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_kms.cc#L919-L979), [rgw_kms.cc:917-977](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_kms.cc#L917-L977),
       [rgw_kms.cc:255-271](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_kms.cc#L255-L271), [rgw_kms.cc:255-271](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_kms.cc#L255-L271)).
  3. SSE-C keeps phase 1's 501 until phase 4. B15's "a default naming no algorithm refuses
     nothing" applies only to a rule with neither a key id nor BucketKeyEnabled true; one with
     either is `aws:kms` (item 1).
  4. CopyObject is not among radosgw's callers above. UploadPart is, so item 1's refusals reach
     a part, but a part takes its encryption from its upload, never from the default:
     `get_encrypt_filter` prepares a part with its upload's attrs through
     `rgw_s3_prepare_decrypt`
     ([rgw_rest_s3.cc:2859-2876](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2859-L2876),
     [rgw_rest_s3.cc:2984-3002](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2984-L3002)).
     PR (b) first reads how CopyObject chooses its destination's encryption at both tags and
     cites it in its notes, then replaces phase 1's 501 in both with that behaviour.
- **Specs:** every refusal of the two functions; TLS and plain HTTP, per floor; the policy keys
  with and without a default; a default without an algorithm, alone and with a key id or a
  bucket key; an UploadPart under a default item 1 refuses; an undecodable default after a 403.
- **Exclusions and registry:** retires the SSE-S3 and SSE-KMS half of "Writes that ask for
  encryption answer 501, SSE-S3 and SSE-KMS until phase 2 and SSE-C until phase 4." and "A
  PUT's encryption condition keys leave out the bucket's default encryption."; extends "PutObject
  and CopyObject authorize a request before they read what the store holds for it" with the
  undecodable default (B16).
- **Depends on:** BC-23; BC-27 (a), whose recorded statuses the no-backend specs pin; the
  write-path unit on the same files: PR (a) after WP-6 and WP-7, PR (b) after WP-6, WP-7 and
  WP-13 (index Order 3), never in parallel with V11, L5b or C8 on a file they share.
- **PR split:** (a) the helper, `CryptAttrs` and `cryptKeys`, with PutObject and
  CreateMultipartUpload; (b) CopyObject and UploadPart. **Brief:** about 240 lines.

### Task BC-27: Encryption recording and gate

- **Layer:** integration. **Files:** the T unit's `hack/s3tests/include-phase2.txt` (this
  unit's nine lines), both ledgers, both known-differences files; new
  `test/integration/phase2_encryption_test.go`. **Cluster:** yes.
- **Behaviour:**
  1. **(a) radosgw's write answers.** On an unconfigured zone, over TLS and over plain HTTP,
     record radosgw's status and error code for PutObject, CreateMultipartUpload and POST
     object into buckets whose default is `AES256` and `aws:kms`, and for the same writes asking
     for SSE-S3 and SSE-KMS by header. The table goes into the registry's key-management notes;
     BC-26's specs pin it. This half needs nothing from rgw-go.
  2. **(b) the configuration tests.** Add test_put/get/delete_bucket_encryption_s3 and _kms
     ([test_s3.py:15390-15487](https://github.com/ceph/ceph/blob/ab228b1b9f0dc8cbc52f6dcbc649519212421c8d/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L15390-L15487),
     helpers
     [test_s3.py:15352-15387](https://github.com/ceph/ceph/blob/ab228b1b9f0dc8cbc52f6dcbc649519212421c8d/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L15352-L15387),
     at the s3-tests pin) to the include list (B6) and record both ledgers. They get no
     known-differences line: none sends or reads BucketKeyEnabled or an off-list value, so both
     gateways agree, and a line would fail as stale.
  3. **(b) BC-25's three tests.** Add include lines for (a), (b) and (c), each with a plain
     known-differences line in both files: (a) and (c) under "A PutBucketEncryption document is
     held to S3's schema and AWS's values", (b) under "GetBucketEncryption writes
     BucketKeyEnabled as true".
  4. **(b) gate case 4:** each gateway writes `AES256` and `aws:kms` configurations the other
     reads, the attr bytes equal and equal to the corpus encoding; rgw-go's write answers equal
     the table of (a). An `aws:kms` configuration with BucketKeyEnabled true is among them: its
     attr bytes are equal across the gateways, and the GetBucketEncryption bodies are equal but
     for radosgw's `True` and rgw-go's `true`.
  5. The five BlockedEncryptionTypes tests at the pin, test_bucket_blocks_sse_c_by_default,
     test_bucket_block_sse_c, test_bucket_block_sse_c_copy, test_bucket_unblock_sse_c and
     test_bucket_block_sse_c_multipart
     ([test_s3.py:15511-15629](https://github.com/ceph/ceph/blob/ab228b1b9f0dc8cbc52f6dcbc649519212421c8d/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L15511-L15629)),
     stay out under the `encryption` marker: both floors fail them, and rgw-go refuses their
     PutBucketEncryption with 400.
- **Specs:** the gate cases above, labelled `integration, rgw-go, phase2, encryption`.
- **Depends on:** (a) nothing; (b) BC-23 to BC-26 and WP-23 (the include list).
  **PR split:** (a) the recording; (b) the include lines, the ledgers and the gate; each landed
  by the controller. **Brief:** about 90 lines.

**Ownership controls: BC-28a to BC-28f (B7, P4 the owner, 2026-10-13).** Neither floor has the
feature. Its only traces there are the IAM action names
([rgw_iam_policy.cc:84](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L84), [rgw_iam_policy.cc:117](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L117),
[rgw_iam_policy.cc:84](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L84), [rgw_iam_policy.cc:120](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L120)), which phase 1
already has (`internal/policy/action.go:39-40`, `:86-87`). `ownershipControls` is no
sub-resource and no op branch tests it, so a floor radosgw serves the GET as ListObjects, the
PUT as CreateBucket and the DELETE as DeleteBucket
([rgw_common.cc:918-976](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L918-L976),
[rgw_common.cc:931-989](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L931-L989)). The behaviour is v21.1.2's, the
first release line to carry it (b3733296c8e), cited at that tag.

### Task BC-28a: Ownership type

- **Layer:** library. **Files:** create `internal/acl/ownership.go`. **Cluster:** no.
- **Produces:** `type Ownership uint8` with `BucketOwnerEnforced` 0, `BucketOwnerPreferred` 1,
  `ObjectWriter` 2; `AttrOwnershipControls` (`user.rgw.ownership-controls`); Encode and Decode;
  `ParseOwnershipXML(body []byte) (Ownership, error)` and its rendering;
  `ParseOwnership(s string) (Ownership, error)`, the name lookup BC-28d's header uses;
  `OwnershipOf(attrs map[string][]byte) Ownership`.
- **Behaviour:**
  1. The enum is a u8 inside ENCODE_START(1,1)
     ([rgw_object_ownership.h:29-33](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.h#L29-L33), [rgw_object_ownership.cc:77-89](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L77-L89)).
  2. `OwnershipOf` reads a missing or undecodable attr as ObjectWriter
     ([rgw_object_ownership.cc:91-106](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L91-L106)).
  3. XML: `OwnershipControls/Rule/ObjectOwnership`, the three names exactly; a missing Rule or
     ObjectOwnership, or another name, is refused with v21's message
     ([rgw_object_ownership.cc:22-75](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L22-L75)). The document renders in the S3 namespace.
- **Specs:** the three values' bytes; a truncated attr read as ObjectWriter; each XML refusal.
- **Exclusions and registry:** new exclusion "A PutBucketOwnershipControls document is held to S3's
  schema" (the strict parse's departures from v21.1.2); no registry entry.
- **Depends on:** nothing. **PR:** one. **Brief:** about 90 lines.

### Task BC-28b: Ownership dispatch

- **Layer:** handler (dispatch). **Files:** modify `internal/s3/dispatch.go` (rows and
  `payloadTable`). **Cluster:** no.
- **Produces:** the routes `get_bucket_ownership_controls`, `put_bucket_ownership_controls`,
  `delete_bucket_ownership_controls` at bucket scope.
- **Behaviour:**
  1. A request naming `ownershipControls` dispatches to the three routes in v21's position,
     after the encryption rows and ahead of each method's fall-through
     ([rgw_rest_s3.h:772-774](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.h#L772-L774); [rgw_rest_s3.cc:5471-5472](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L5471-L5472),
     [rgw_rest_s3.cc:5531-5532](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L5531-L5532), [rgw_rest_s3.cc:5558-5559](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L5558-L5559)).
     It is matched as a query argument, not a signing sub-resource, as v21 tests it.
  2. The PUT accepts a signed payload as the other configuration PUTs do.
  3. Until BC-28d binds them the routes answer 501 after authentication, phase 1's rule for an
     unbound route (`internal/s3/handler.go:377-381`), so no request reaches another op.
- **Specs:** each method's dispatch with and without other sub-resources; the DELETE on an
  empty bucket leaves it in place.
- **Exclusions and registry:** new registry entry for defect 11 (S3-correctness, a bucket lost;
  unreproduced; the feature lands in v21.0.0); new exclusion "ownershipControls requests are
  served as v21 serves them", but for the PUT body's parse, which BC-28a's exclusion "A
  PutBucketOwnershipControls document is held to S3's schema" records and this one points to.
- **Depends on:** nothing. **PR:** one. **Brief:** about 60 lines.

### Task BC-28c: Ownership ops

- **Layer:** op. **Files:** create `internal/op/ownership.go`; modify
  `internal/op/bucketsubres.go` (PutBucketACL), `internal/op/objectattrs.go` (PutObjectACL),
  `internal/op/bucketops.go` (CreateBucket). **Cluster:** no.
- **Consumes:** BC-28a. **Produces:** `GetBucketOwnershipControls{Ownership}`,
  `PutBucketOwnershipControls{Params}`, `DeleteBucketOwnershipControls`; the errors
  AccessControlListNotSupported (400), InvalidBucketAclWithObjectOwnership (400) and
  OwnershipControlsNotFoundError (404) ([rgw_common.cc:142-144](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_common.cc#L142-L144));
  `CreateBucket.ObjectOwnership *acl.Ownership`.
- **Behaviour** ([rgw_op.cc:10247-10358](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10247-L10358)):
  1. **Put.** Action s3:PutBucketOwnershipControls. BucketOwnerEnforced on a bucket whose ACL
     is not the default private one is 400 InvalidBucketAclWithObjectOwnership, "The bucket's
     ACL must be made private before setting BucketOwnerEnforced."; an ACL that does not decode
     is 500. It writes only the ownership attr on each try, authorized again (B9).
  2. **Get.** Action s3:GetBucketOwnershipControls; no attr is OwnershipControlsNotFoundError,
     "The bucket ownership controls were not found"; an undecodable one is 500.
  3. **Delete.** Authorized as s3:PutBucketOwnershipControls; removes the attr; success when
     absent.
  4. **ACL writes.** PutBucketAcl and PutObjectAcl under BucketOwnerEnforced are 400
     AccessControlListNotSupported, "Cannot set ACLs when ObjectOwnership is
     BucketOwnerEnforced." ([rgw_op.cc:6866-6871](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L6866-L6871)).
  5. **CreateBucket.** An ownership value needs s3:PutBucketOwnershipControls besides
     s3:CreateBucket, else 403 ([rgw_op.cc:3754-3759](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L3754-L3759)), and is stored in the new
     bucket's attrs ([rgw_op.cc:4233-4237](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L4233-L4237)).
- **Specs:** each op's error order with fakes; the private-ACL comparison against a
  radosgw-shaped default ACL; the ACL refusals on both ACL ops; CreateBucket with and without
  the permission.
- **Exclusions and registry:** new exclusion "rgw-go stores and enforces ownership controls a
  floor radosgw ignores": a floor radosgw accepts ACL writes under BucketOwnerEnforced and
  carries the attr through its attr merges. Its Put and Delete writes join defect 5's exclusion
  and "A retried bucket write stops when the name names another bucket". The floors
  serve no ownership ops, so they cite v21.1.2's retries, which re-read the bucket by name and
  authorize nothing ([rgw_op.cc:10277-10304](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10277-L10304),
  [rgw_op.cc:10353-10357](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10353-L10357)).
- **Depends on:** BC-28a. **PR split:** (a) the three ops; (b) the ACL refusals and
  CreateBucket, after L2's PR (b) on `bucketops.go`. **Brief:** about 200 lines.

### Task BC-28d: Ownership routes

- **Layer:** handler. **Files:** S2's `internal/s3/ownership.go`; modify the CreateBucket
  handler in `internal/s3/bucket.go` (one header read). **Cluster:** no.
- **Consumes:** BC-28a's `acl.ParseOwnershipXML(body []byte) (acl.Ownership, error)`,
  `(acl.Ownership).MarshalS3XML() []byte` and the exported name lookup
  `acl.ParseOwnership(s string) (acl.Ownership, error)`; BC-28c's ops and
  `CreateBucket.ObjectOwnership`.
- **Behaviour** ([rgw_rest_s3.cc:4553-4618](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4553-L4618)):
  1. PUT reads the body up to `rgw_max_put_param_size`; a parse failure, no `OwnershipControls`
     or a refused rule is 400 MalformedXML with the Message of the refusal's `*acl.ParseError`,
     v21's text or none; 200 with an empty body. The handler makes that `op.ErrMalformedXML`
     itself: `authz.ErrorFor` maps no `acl.ErrMalformedXML` (`internal/authz/errors.go:36-63`),
     and the same name lookup is InvalidArgument in the header (3).
  2. GET 200 with the document; DELETE 204.
  3. CreateBucket's `x-amz-object-ownership` header becomes the op's value through
     `acl.ParseOwnership`; a value v21 does not parse is 400 InvalidArgument with its message
     ([rgw_rest_s3.cc:2809-2816](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L2809-L2816)),
     which the refusal's `*acl.ParseError` carries with `acl.ErrInvalid`, as `authz.ErrorFor`
     maps it (`internal/authz/errors.go:52-53`).
- **Specs:** each route's status, headers and body, from the eight `object_ownership` s3-tests.
- **Exclusions and registry:** none.
- **Depends on:** S2, BC-28b, BC-28c. **PR:** one, after L2's PR (b) on `internal/s3/bucket.go`.
  **Brief:** about 80 lines.

### Task BC-28e: Ownership enforcement

- **Layer:** authz. **Files:** modify `internal/authz/load.go` (the ownership with the bucket
  inputs), `evaluator.go`, `write.go` (`createS3Policy`); `internal/acl/ownership.go`
  (`CannedFor`, beside `acl.Canned`, `internal/acl/s3xml.go:414`); one call in
  `internal/op/postobject.go`. **Cluster:** no.
- **Consumes:** BC-28a's `OwnershipOf`. **Produces:** `acl.CannedFor(owner, bucketOwner acl.Owner,
  own acl.Ownership, canned string) (acl.Policy, error)`.
- **Behaviour:**
  1. **Evaluation.** Under BucketOwnerEnforced an object ACL grants nothing
     ([rgw_common.cc:1630-1638](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_common.cc#L1630-L1638)), read from
     the bucket's attrs as v21 loads it ([rgw_op.cc:682](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L682)).
  2. **Canned ACLs on writes.** Under BucketOwnerEnforced only `bucket-owner-full-control` or no
     canned ACL is accepted, else 400 AccessControlListNotSupported, and the bucket owner owns
     the object; under BucketOwnerPreferred the bucket owner owns it for
     `bucket-owner-full-control`; under ObjectWriter the writer does
     ([rgw_acl_s3.cc:678-706](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl_s3.cc#L678-L706)). PutObject, CopyObject,
     CreateMultipartUpload and POST object all take it.
  3. **AWS where v21 stops short.** v21 ignores only object ACLs, and builds a grant-header ACL
     without looking at the ownership ([rgw_rest_s3.cc:2629-2644](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L2629-L2644)).
     The task quotes AWS's "Controlling ownership of objects and disabling ACLs" page, with its
     URL, in its notes. Where that page says ACLs no longer affect permissions or are refused,
     the bucket ACL and grant headers follow it, each difference from v21 an exclusion.
- **Specs:** an object ACL granting public-read ignored under BucketOwnerEnforced and honoured
  under ObjectWriter; each canned ACL under each setting; the object owner for each; a
  grant header under BucketOwnerEnforced per the quoted page.
- **Exclusions and registry:** the exclusions of item 3, if any.
- **Depends on:** BC-28a, S3 (on the same authz files), BC-32 (whose canned ACL it switches).
  **PR split:** (a) evaluation; (b) the write ACLs. **Brief:** about 180 lines.

### Task BC-28f: Ownership recording

- **Layer:** integration. **Files:** the s3-tests marker list (`object_ownership` removed),
  both ledgers, the known-differences files; new `test/integration/phase2_ownership_test.go`.
  **Cluster:** yes.
- **Behaviour:**
  1. Remove `object_ownership` from the excluded markers (B6) and record both ledgers. The
     eight tests (`test_s3.py:21557-21662` at the pin) are predicted to fail on radosgw at both
     floors and pass on rgw-go; each becomes a known-differences line under BC-28c's exclusion
     once the recording confirms it.
  2. Gate case 7: rgw-go's attr survives a radosgw PutBucketAcl and PutBucketTagging and is
     read back by rgw-go.
  3. Reproduce defect 11 on both floors (DELETE `?ownershipControls` on an empty bucket).
- **Specs:** the gate case above, labelled `integration, rgw-go, phase2, ownership`.
- **Depends on:** BC-28a to BC-28e. **PR:** one, landed by the controller. **Brief:** about 60
  lines.

**POST object: BC-29 to BC-34.** POST object has no Authorization header, so radosgw makes it
anonymous and its suspended-user check passes it
([rgw_process.cc:372-376](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L372-L376), [rgw_process.cc:374-378](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L374-L378)). The form
is read and its credentials authenticated in `init_processing`, after the bucket loads and
before the op mask and the permission check
([rgw_op.cc:4576-4590](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4576-L4590),
[rgw_op.cc:4869-4883](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4869-L4883)). Review Focus 5 lands in
BC-29's and BC-32's specs.

### Task BC-29: Form reader

- **Layer:** handler (parser). **Files:** create `internal/s3/postform.go`. **Cluster:** no.
- **Produces:** `newFormReader(body io.Reader, contentType string, maxLine int) (*formReader,
  error)`; `(*formReader).Fields() (formFields, *formFile, error)`, the fields before the file
  part and the file part, which carries its filename and reads its data; `(*formReader).Drain()
  error` for the parts after it.
- **Behaviour** (radosgw's reader is identical at both tags,
  [rgw_rest.cc:1112-1476](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1112-L1476), [rgw_rest.cc:1112-1476](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1112-L1476)):
  1. The Content-Type must be exactly `multipart/form-data`, else 400 InvalidArgument "Request
     Content-Type is not multipart/form-data"; a missing `boundary` is "Missing multipart
     boundary specification" ([rgw_rest.cc:1431-1473](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1431-L1473),
     [rgw_rest.cc:1431-1473](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1431-L1473);
     [rgw_rest.cc:1112-1145](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1112-L1145),
     [rgw_rest.cc:1112-1145](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1112-L1145)).
  2. Part header lines are trimmed and bounded by `maxLine` (`rgw_max_chunk_size`), and the
     Content-Disposition `name` names the part
     ([rgw_rest.cc:1311-1370](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1311-L1370),
     [rgw_rest.cc:1311-1370](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1311-L1370)). Names
     compare as `postpolicy.Env` compares them, in any ASCII case over all their bytes, where
     radosgw's `ltstr_nocase` part map stops at a NUL
     ([rgw_rest.h:246-247](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.h#L246-L247), [rgw_rest.h:246-247](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.h#L246-L247));
     a later field of the same name replaces the earlier's value and keeps its spelling, as both
     the part map and `RGWPolicyEnv::add_var` do. **A part name holding a NUL, CR, LF or any
     other control byte is 400 InvalidArgument**, an exclusion: radosgw keeps such a name whole
     in its part map, so a NUL name aliases a shorter one and reaches Tentacle's checksum
     exception (`docs/ceph-upstream-bugs.md`, "Tentacle's POST policy excepts a field whose name
     holds a NUL after a checksum algorithm's"), and kept whole in rgw-go an `x-amz-meta-` name
     would reach an xattr name, which go-ceph passes as a C string, and a GET's response header
     (BC-30's review).
     A field keeps its raw value for the policy environment and gives a trimmed one for
     parameters ([rgw_rest.cc:1372-1385](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1372-L1385),
     [rgw_rest.cc:1372-1385](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1372-L1385)).
  3. Parts are read up to the one named `file`, case-insensitively, keeping its Content-Disposition
     `filename`. The end of the form before it is 400 "Malformed request"; a field whose data has
     no delimiter within `maxLine` bytes is 400 "Couldn't find boundary"
     ([rgw_rest_s3.cc:2914-2971](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2914-L2971),
     [rgw_rest_s3.cc:3040-3097](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3040-L3097)).
  4. The file part streams: it is never buffered whole, and memory stays bounded by a few
     reads whatever its size.
  5. **Delimiter (B11).** A part ends only at a delimiter line, CRLF `--` boundary at a line
     start followed by CRLF or `--`, as RFC 2046 §5.1.1 defines it for `multipart/form-data`
     (RFC 7578). **Differs from radosgw by design:** radosgw's search matches `--` boundary
     anywhere in the data ([rgw_rest.cc:1173-1220](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1173-L1220),
     [rgw_rest.cc:1173-1220](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1173-L1220)), so file data holding the string mid-line
     is cut there (defect 18).
  6. `Drain` reads the parts after the file and stores nothing
     ([rgw_rest_s3.cc:3256-3278](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3256-L3278),
     [rgw_rest_s3.cc:3392-3414](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3392-L3414)).
- **Specs:**
  - the framing cases: CRLF line ends, a delimiter split across two reads, the final `--`,
    parts after the file, a missing boundary, the end before the file;
  - each "Malformed request" and "Couldn't find boundary";
  - two fields whose names differ only in ASCII case, the later's value kept under the first's
    spelling; names differing in a non-ASCII letter's case kept apart;
  - a part name holding a NUL, a CR, an LF, a tab or DEL refused, among them Tentacle's two
    exploit shapes, `acl` then a NUL, eleven bytes, `crc32`, a NUL and `x-amz-checksum-`, and
    `x-amz-meta-aaaacrc32` then a NUL and `x-amz-checksum-`;
  - **Review Focus 5, a boundary string inside the file data:** file data holding `--` boundary
    mid-line, and the boundary text without its leading CRLF, reads back byte-identical;
  - **Review Focus 5, a file larger than memory:** a generated multi-GiB file part, never held
    in memory, read through the file reader into a discarding sink, with the heap's growth
    under a fixed multiple of the read size;
  - an s3-tests addition for the boundary case, to the `rgw-go-s3tests` branch.
- **Exclusions and registry:** new exclusion for the delimiter rule; new exclusion for the
  refused part names; new registry entry for defect 18 (S3-correctness: an upload cut at the
  string, stored truncated or refused; unreproduced, from the source).
- **Depends on:** BC-30, whose `postpolicy.Env` item 2's name rule matches. **PR:** one.
  **Brief:** about 215 lines.

### Task BC-30: POST policy document

- **Layer:** library. **Files:** create `internal/postpolicy/`. **Cluster:** no.
- **Consumes:** WP-1's `cksum.ParseName`. **Produces:** `Parse(doc []byte) (*Policy, error)`;
  `Env`, case-insensitive with a checked mark per variable; `(*Policy).Check(env *Env, now
  time.Time) error`; `MinLength` and `MaxLength`.
- **Behaviour** ([rgw_policy_s3.cc:16-305](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L16-L305), [rgw_policy_s3.cc:20-314](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L20-L314)):
  1. **Parse.** Each refusal is 400 with radosgw's message: malformed JSON, no `expiration`, an
     expiration `parse_iso8601` refuses, no `conditions`, an array condition without exactly
     three elements, an operator other than `eq`, `starts-with` or `content-length-range`, a
     bad length; an object condition checks its first member only
     ([rgw_policy_s3.cc:238-305](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L238-L305),
     [rgw_policy_s3.cc:247-314](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L247-L314);
     [rgw_policy_s3.cc:132-187](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L132-L187),
     [rgw_policy_s3.cc:141-196](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L141-L196)). The JSON is read with
     Go's decoder, and ceph's JSONParser's handling of duplicate keys and number text is pinned
     by specs.
  2. **Conditions.** `eq` is exact, `starts-with` a prefix, `content-length-range` tightens the
     bounds; `$name` reads a variable case-insensitively and marks it checked
     ([rgw_policy_s3.cc:53-102](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L53-L102), [rgw_policy_s3.cc:57-106](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L57-L106)).
  3. **Check**, each 403 with radosgw's message: expired (expiry at or before now); a simple
     check whose field is missing or differs; a failed condition; a field no condition checked,
     `x-ignore-*` excepted ([rgw_policy_s3.cc:189-235](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L189-L235),
     [rgw_policy_s3.cc:198-244](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L198-L244);
     [rgw_policy_s3.cc:105-121](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L105-L121)).
  4. **Differs:** v20.2.4 also excepts checksum fields
     ([rgw_policy_s3.cc:108-130](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L108-L130)); rgw-go
     excepts them on both releases (B13), naming them by `cksum.ParseName`.
- **Specs:** every s3-test policy shape; every message; the expiry boundary; a duplicate key
  and an exponent number as ceph's parser reads them; a checksum field on each release.
- **Exclusions and registry:** new exclusion for the checksum exception on Squid (B13).
- **Depends on:** WP-1. **PR:** one. **Brief:** about 180 lines.

### Task BC-30b: One parse_iso8601 and one json_spirit reader

- **Layer:** library. **Files:** create `internal/jsonspirit/` from `internal/meta/json_spirit.go`;
  move `parse_iso8601`'s extended format out of `internal/op/readconds.go` into
  `internal/strptime` or `internal/rgwtext`, whichever adds no import cycle (strptime imports
  only `time`, rgwtext only `math` and `strings`); modify `internal/meta/json_spirit.go`,
  `internal/meta/jsonformattable.go`, `internal/op/readconds.go` and
  `internal/postpolicy/{expiration,json,number}.go`. **Cluster:** no.
- **Why:** BC-30's Files line confined it to `internal/postpolicy/`, so it transcribed two things
  rgw-go already had: `parse_iso8601`'s extended format (`internal/postpolicy/expiration.go:23-43`
  beside `internal/op/readconds.go:318-348`) and json_spirit's number reading with the glibc
  `pow` table (`internal/postpolicy/number.go:25-138` beside
  `internal/meta/json_spirit.go:313-514`). The copies have already drifted: meta refuses a real
  whose integer part has more than 300 digits, where postpolicy, closer to radosgw, accumulates
  it as boost's `positive_accumulate` does until the double overflows, which involves no libm
  call (BC-30's review).
- **Produces:** an exported `parse_iso8601` (extended format) returning the `strptime.Tm` and the
  fraction's nanoseconds, which `op.ParseHTTPTime` and `postpolicy.Parse` call on their own
  C-string-cut input; `internal/jsonspirit`, json_spirit's reader as JSONParser reads it, with a
  node's `get_data` text and its children in JSONObj's order, which `meta` and `postpolicy` call.
- **Behaviour:**
  1. **One parse_iso8601**, identical at both tags
     ([rgw_common.cc:599-659](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L599-L659),
     [rgw_common.cc:612-672](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L612-L672)).
     Each caller keeps its own use of the result: `op` its nanoseconds and `utime`'s 32-bit
     seconds, `postpolicy` the whole seconds `internal_timegm` gives. `auth`'s basic-format
     reader is a different format and stays.
  2. **One number reader**, `number_` as `meta` transcribes it, with postpolicy's rule for an
     integer part: any length until the accumulated double passes DBL_MAX. The limits that
     depend on the host's libm stay: an exponent past 300, a fraction of more than 300 digits,
     the exponent 126, a value that is not finite.
  3. **The POST policy is read with the shared reader**, bounded at 10000 levels of nesting, in
     place of Go's decoder. The bound is needed on both callers' paths: the reader recurses once
     per level, so an unbounded nesting would grow the goroutine's stack toward Go's 1 GB
     default limit, where the runtime ends the process. The review's differential fuzz of
     BC-30's tree against `meta`'s transcription, 120 s and 1,851,614 inputs, found the two
     agree on every document both accept but for an escaped surrogate pair, so the change keeps
     every BC-30 reading and adds json_spirit's: stray commas, C white space, raw control and
     non-UTF-8 bytes, dropped and non-hex escapes, signs, leading zeros and bare dots, and each
     half of a surrogate pair written alone. `\x` and octal escapes, which `meta` refuses as not
     transcribed, stay refused unless the task transcribes boost's `escape_char_parse`. If the
     task finds a document the shared reader cannot read as JSONParser does, it keeps Go's
     decoder for the policy and records why in the exclusion.
- **Specs:** every spec of the three packages passes unchanged but where behaviour 2 or 3
  changes it: `meta` takes a real with a 301-digit integer part; each of BC-30's grammar
  refusals becomes a spec of json_spirit's reading of the same document; 10000 levels read and
  10001 refused on the shared reader, and a 4 MiB nesting refused without its stack growing past
  a fixed bound; BC-30's `FuzzParse` kept, with a seed for each grammar case.
- **Exclusions and registry:** shrink "A POST policy is read as RFC 8259 JSON" to what remains,
  retitled to match; no registry change.
- **Depends on:** BC-30. **PR:** one. **Brief:** about 130 lines.

### Task BC-31: Browser-upload credentials

- **Layer:** auth. **Files:** create `internal/auth/browserupload.go`; modify
  `internal/s3/auth.go` (the interface). **Cluster:** no.
- **Produces:** `op.BrowserUploadCredentials` and
  `Authenticator.AuthenticateBrowserUpload` (signatures in Seams).
- **Behaviour:**
  1. **SigV2** signs the base64 policy with HMAC-SHA1 of the secret. **SigV4** takes the key id
     from `x-amz-credential` up to its first `/`, the rest as the scope, and signs the base64
     policy with that scope's signing key. Neither checks the date or the region
     ([rgw_rest_s3.cc:6158-6212](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L6158-L6212), [rgw_rest_s3.cc:6688-6742](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6688-L6742)).
     An empty key or signature is refused. Signatures compare in constant time.
  2. The key's lookup and the identity are the header path's
     (`internal/auth/verifier.go:147-181`, `internal/auth/identity.go:30`): the identity, its
     user record and its identity policies replace the anonymous ones, as radosgw's strategy
     does ([rgw_auth.cc:489-555](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth.cc#L489-L555),
     [rgw_auth.cc:504-570](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth.cc#L504-L570)).
  3. A session token is treated as the header path treats one.
  4. A failed authentication is 403, as radosgw's -EACCES from `get_policy`.
- **Specs:** SigV2 vectors; AWS's published SigV4 POST policy example; a bad key, a bad
  signature, an inactive key; an s3-tests addition for a SigV4 POST, which the pin lacks.
- **Exclusions and registry:** none.
- **Depends on:** nothing. **PR:** one. **Brief:** about 150 lines.

### Task BC-32: POST object op

- **Layer:** op. **Files:** create `internal/op/postobject.go`. **Cluster:** no.
- **Consumes:** BC-26's `EncryptionDefaults`; BC-30; WP-5 and WP-6's checksum request; V11's
  `VersionForWrite` once it lands (Seams). **Produces:** `PostObject` with two hooks the handler
  sets: `Form` (BC-29's fields and file) and `Authenticate` (BC-31).
- **Behaviour.** Init runs `RGWPostObj_ObjStore_S3::get_params`'s order
  ([rgw_rest_s3.cc:2914-3078](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2914-L3078),
  [rgw_rest_s3.cc:3040-3214](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3040-L3214)):
  1. The bucket loads (NoSuchBucket first), then `Form`.
  2. The `x-amz-server-side-encryption*` fields go to `EncryptionDefaults` as the asked
     encryption. Every field enters the policy environment with its raw value, then `bucket`.
     **Every field the op reads is read through `postpolicy.Env`'s name rule** (BC-29 item 2):
     `key`, `acl`, `Content-Type`, `x-amz-meta-*`, `x-amz-storage-class`, `tagging`,
     `success_action_*`, the SSE fields, the checksum fields and
     `x-amz-website-redirect-location`, each with the last duplicate's value, which is the value
     `Check` judged. A map or loop of the op's own that compares names otherwise, or keeps the
     first duplicate, writes a value the policy never checked (BC-30's review).
  3. `key` is required (400 "Key not specified"); its first `${filename}` is replaced
     ([rgw_rest_s3.cc:2889-2902](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2889-L2902),
     [rgw_rest_s3.cc:3015-3028](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3015-L3028)); an empty
     result is 400 "Empty object name". Then `key` enters the environment as B12 rules; radosgw
     enters the replaced key ([rgw_rest_s3.cc:2999-3006](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2999-L3006),
     [rgw_rest_s3.cc:3137-3144](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3137-L3144)).
  4. `Content-Type` enters the environment when set; `x-amz-storage-class` must be a valid
     placement ("The storage class you specified is not valid"); `x-amz-meta-*` and
     `x-amz-website-redirect-location` become NUL-terminated attrs. Their names hold no NUL, CR,
     LF or other control byte, since BC-29 refuses such a part name; the op relies on that and
     does not re-check it.
  5. Credentials ([rgw_rest_s3.cc:3120-3254](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3120-L3254),
     [rgw_rest_s3.cc:3256-3390](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3256-L3390)): with a
     `policy` field, `x-amz-algorithm` AWS4-HMAC-SHA256 selects SigV4, which needs
     `x-amz-credential`, `x-amz-signature` and `x-amz-date`; otherwise SigV2 needs
     `AWSAccessKeyId` and `signature`; each missing field is 400 with radosgw's message; an
     empty `x-amz-security-token` is 400 "Invalid token". `Authenticate` then resolves the
     identity. Without a `policy` field the request stays anonymous.
  6. **A suspended user is 403 UserSuspended** once `Authenticate` resolves it (B11, defect 8).
  7. The policy decodes (400 "Could not decode policy") and BC-30's `Check` runs, the
     signature fields marked checked as radosgw marks them. The `acl` field is a canned ACL
     built by `acl.Canned` ("Bad canned ACLs"), and `tagging` a Tagging document
     ([rgw_rest_s3.cc:3080-3118](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3080-L3118),
     [rgw_rest_s3.cc:3216-3254](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3216-L3254)).
  8. No Content-Length is 411 MissingContentLength, and one over `rgw_max_put_size` is 400
     EntityTooLarge ([rgw_rest.cc:1413-1429](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1413-L1429),
     [rgw_rest.cc:1413-1429](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1413-L1429)).

  VerifyPermission is s3:PutObject on the key's ARN, with the effective encryption in the
  environment ([rgw_op.cc:4592-4605](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4592-L4605),
  [rgw_op.cc:4885-4898](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4885-L4898)). Execute follows
  [rgw_op.cc:4612-4796](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4612-L4796) and
  [rgw_op.cc:4905-5139](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4905-L5139):
  9. **A public canned ACL under BlockPublicAcls is 403 AccessDenied**, as rgw-go's PutObject
     refuses one (`internal/op/putobject.go:157-168`; B11, defect 9). The op judges the ACL it
     builds from the form's `acl` field, item 7's canned ACL marked for the writer's own write
     by `ForOwnWrite` as `authz.DefaultACL` marks PutObject's, with `acl.Policy.IsPublic`,
     the AWS-correct judgment of a public ACL that PutObject, CopyObject, CreateMultipartUpload
     and UploadPart apply under the block (`internal/op/putobject.go:166`,
     `internal/op/copyobject.go:219`, `internal/op/initmultipart.go:100`); it never tests the
     canned name alone. radosgw checks no block on POST object at either release, and copying
     RGWPutObj's test would not fix that, since it tests the `x-amz-acl` header, which never
     holds the form's field (`docs/ceph-upstream-bugs.md`, "radosgw lets a public ACL through
     a block of public ACLs on PutObject's grant headers, CopyObject and
     CreateMultipartUpload").
  10. The quota check; Content-MD5 (400 InvalidDigest or BadDigest); the versioned write
     (`VersionForWrite`, or phase 1's 501 until V11); BC-26's encryption; the checksum field
     through WP-6's parser, D21's refusals included; `ObjectStore.PutObject` streaming the file
     with the policy's bounds (400 EntityTooLarge over the maximum, EntityTooSmall under the
     minimum); the attrs ETag, ACL, Content-Type (NUL-terminated), metas, the redirect and tags.
     **Every checksum field D21 refuses reaches WP-6's parser and is refused,** whatever the
     policy said of it: `x-amz-checksum-md5`, `-xxhash64`, `-xxhash128`, `-xxhash3`, Tentacle's
     spelling `-xxh3` and `-blake3`, an `x-amz-checksum-algorithm` naming any of them, and each
     of them when a form holds several checksum fields. `-xxhash3`, `-xxh3`, `-blake3` and the
     algorithm field pass `Check` with no condition, since `cksum.ParseName` names them; `-md5`,
     `-xxhash64` and `-xxhash128` pass only when a condition names them, since `Check` excepts
     no name `ParseName` refuses. A field the write does not refuse is ignored, so the refusal
     is what stops it. **The bounds compare as radosgw's do:** `MinLength` and
     `MaxLength` are signed, as its `off_t` fields are
     ([rgw_op.h:1328-1331](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1328-L1331),
     [rgw_op.h:1406-1409](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1406-L1409)),
     and may be negative or the maximum below the minimum; the maximum is compared after each
     non-empty read and the minimum once after the stream
     ([rgw_op.cc:4705-4732](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4705-L4732),
     [rgw_op.cc:5018-5046](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5018-L5046)),
     so an empty file passes even a negative maximum.
     Default retention comes from the head write, as for PutObject
     ([rgw_rados.cc:3211-3215](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3211-L3215),
     [rgw_rados.cc:3308-3312](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3308-L3312)). **Differs:** v20.2.4 adds the
     streaming checksum and `user.rgw.cksum`; rgw-go serves it on both releases (D22).
- **Specs:**
  - the error order across Init, VerifyPermission and Execute, with counterfeiter fakes;
  - each length bound; each attr; a suspended user; a public canned ACL under a block;
  - each field of item 2 sent twice, in two ASCII cases, with the policy's condition met by the
    later value: the later value is the one written;
  - each checksum field D21 refuses, alone, beside a CRC32 field, and named by
    `x-amz-checksum-algorithm`, under a policy that names none of them: refused by WP-6's
    parser;
  - the bounds: an empty file under a maximum of -1 stored, and a one-byte file refused; a
    maximum of 5 under a minimum of 10 refusing every file, the empty one too; a file of exactly
    each bound stored;
  - **Review Focus 5, a `$key` condition with a `${filename}` key:** with key
    `uploads/${filename}` and filename `cat.jpg`, the conditions `["eq", "$key",
    "uploads/${filename}"]`, `["eq", "$key", "uploads/cat.jpg"]` and `["starts-with", "$key",
    "uploads/"]`, each answered as B12 rules;
  - **Review Focus 5, a file larger than memory:** a generated multi-GiB file through the op
    into a fake ObjectStore that hashes and discards, with the heap's growth under a fixed
    multiple of the read size and the ETag the generator's MD5;
  - s3-tests additions for the suspended user, the public ACL under a block and the `$key`
    case, to the `rgw-go-s3tests` branch.
- **Exclusions and registry:** new exclusion and registry entry for defect 8 (security,
  fail-open; unreproduced; prior-art search pending; also sent to the rgw-bug-reproduction
  session); extends "A block of public ACLs refuses a public ACL on PutObject, CopyObject,
  CreateMultipartUpload and UploadPart" with POST object (defect 9), whose registry entry BC-30
  already extended, and updates that entry's rgw-go line to say rgw-go serves POST object and
  refuses there; retires the last sentence of "PutObject and CopyObject authorize a request before
  they read what the store holds for it", which says rgw-go serves no POST object; an exclusion
  for B12 if AWS's page departs from radosgw.
- **Depends on:** BC-26 (a), BC-30, BC-31, WP-5, WP-6. **PR split:** (a) Init and
  VerifyPermission; (b) Execute. **Brief:** about 265 lines, past PP-6's 250 with BC-30's
  review's carries, so one brief per PR if it runs long.

### Task BC-33: POST object routes

- **Layer:** handler. **Files:** S2's `internal/s3/postobject.go`; modify `internal/s3/handler.go`
  (the byte counter's condition, `internal/s3/handler.go:356-358`). **Cluster:** no.
- **Consumes:** BC-29, BC-31 (through the route set's authenticator), BC-32.
- **Behaviour:**
  1. The handler binds BC-32's hooks to the request. `newFormReader` runs inside the `Form`
     hook, so its Content-Type refusals and the 100 Continue come after the bucket loads, where
     radosgw's `get_params` has them ([rgw_rest.cc:1431-1439](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1431-L1439),
     [rgw_rest.cc:1431-1439](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1431-L1439)).
  2. The response ([rgw_rest_s3.cc:3305-3420](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3305-L3420),
     [rgw_rest_s3.cc:3441-3556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3441-L3556)):
     `success_action_redirect` is 303 with Location `<redirect>?bucket=…&key=…&etag=…`
     url-encoded, the tenant first when set; `success_action_status` 200 is 200, 201 is 201
     with a `PostResponse` of Location, Bucket, Key and ETag, anything else 204; the default is
     204 with the ETag header; radosgw's messages go into the error documents. A policy
     refusal's message quotes the policy's operands and the form's field names unbounded: a
     nested operand is written back with each byte past ASCII escaped in six, so a 3 MiB policy
     can give a message of about 18 MiB (BC-30's review). The error document keeps the message
     whole, as radosgw sends it, and what rgw-go logs of it is cut to a fixed bound, which needs
     no exclusion; cutting the document would.
  3. The 201's Location takes its domain as rgw-go's CompleteMultipartUpload does
     (`internal/s3/multipart.go:341-360`; defect 17).
  4. **Differs from both floors by design (B11):** the usage log counts the body, as main does
     ([rgw_rest.cc:1236-1238](https://github.com/ceph/ceph/blob/7d2484c45b4/src/rgw/rgw_rest.cc#L1236-L1238)); the
     byte counter, today PutObject's alone (`internal/s3/handler.go:473-486`), covers post_obj.
- **Specs:** each status and header; `${filename}` end to end; a tenant's redirect; the usage
  entry's bytes received; a policy refusal whose message passes the log bound, sent whole and
  logged cut.
- **Exclusions and registry:** new exclusion and registry entry for defect 10 (S3-correctness;
  reported upstream as tracker #76157; fixed on main by 7d2484c45b4); adds POST object to the
  registry entry defect 17 names.
- **Depends on:** S2, BC-29, BC-32. **PR:** one, after BC-15 on `handler.go`. **Brief:** about
  220 lines.

### Task BC-34: POST recording and gate

- **Layer:** integration. **Files:** `hack/s3tests` deselect lines 55-89 (and
  test_post_object_upload_checksum's line if WP-14 added one), both ledgers; new
  `test/integration/phase2_postobject_test.go`. **Cluster:** yes.
- **Behaviour:**
  1. Delete the 35 deselect lines (all SigV2) and record both ledgers from radosgw before
     comparing.
  2. Gate case 6: an rgw-go POST read by radosgw (GET body, ETag, Content-Type, metadata, ACL,
     tags) and by `radosgw-admin object stat` and `bucket check`; a radosgw POST read by
     rgw-go; the usage log's bytes for each.
  3. Reproduce defects 8, 9, 10 and 18 on both releases, set their registry status, and run
     each entry's prior-art search.
- **Specs:** the gate cases above, labelled `integration, rgw-go, phase2, postobject`.
- **Depends on:** BC-29 to BC-33. **PR:** one, landed by the controller. **Brief:** about 70
  lines.

### Task BC-35: Retitle the re-authorized-retries exclusion

- **Layer:** docs. **Files:** modify `docs/exclusions.md`, `docs/ceph-upstream-bugs.md` and
  this file. **Cluster:** no.
- **Behaviour:**
  1. Retitle "A PutBucketAcl that loses a race is retried, and every retried ACL, policy or
     tagging write is authorized again" to "A PutBucketAcl that loses a race is retried, and
     every retried bucket write is authorized again". The new title names no kind of write,
     so no later retried write renames it, and it keeps the prefix the short quote in item 2
     uses.
  2. Update every verbatim quote of the old title: BC-3's and BC-11's Exclusions and registry
     lines in this file, and `docs/ceph-upstream-bugs.md`'s entries "radosgw's PutBucketAcl
     answers success when its write loses a race" and "radosgw's retried bucket writes are not
     authorized again". The short form "A PutBucketAcl that loses a race is retried" in
     `docs/exclusions.md`'s "A retried bucket write stops when the name names another bucket"
     still holds under the new title.
  3. Announce the rename to the rgw-rs session.
- **Specs:** none. `make citecheck` on the changed files; a search for the old title finds it
  only in plan and research text that records history.
- **Exclusions and registry:** the retitle and the quotes above; no behaviour changes.
- **Depends on:** BC-3, BC-11, BC-18, BC-24, BC-28c, which each extend the exclusion's body,
  so a rename inside any of them would collide with the others and orphan the quotes.
  **PR:** one, docs only. **Brief:** about 40 lines.

## Order

Prerequisites from other units: S2 for every handler task; S3 for BC-18, BC-20 and BC-28e; S5
for BC-18; WP-1 for BC-30; WP-5 and WP-6 for BC-32; WP-6, WP-7 and WP-13 for BC-26 (Hot files);
WP-23 for BC-27 (b); L2's PR (b) for BC-28c (b) and BC-28d. Within the unit:

| Wave | Tasks, in parallel | Each waits for |
|---|---|---|
| 1 | BC-2, BC-5, BC-9, BC-13, BC-17, BC-23, BC-28a, BC-28b, BC-29, BC-31; BC-20; BC-30 | nothing in the unit; BC-20 S3; BC-30 WP-1 |
| 2 | BC-3, BC-10, BC-11, BC-18, BC-21, BC-24, BC-28c (a), BC-30b | BC-2 and BC-5; BC-9; BC-9; BC-17, S3 and S5; BC-20; BC-23; BC-28a; BC-30 |
| 3 | BC-4, BC-7, BC-12, BC-14, BC-19, BC-25, BC-26 (a), BC-28c (b) | BC-3; BC-3; BC-10 and BC-11; BC-10 and BC-13; BC-18; BC-24; BC-23, BC-27 (a), WP-6 and WP-7; BC-28c (a) and L2 (b) |
| 4 | BC-6, BC-15, BC-32, BC-26 (b), BC-28d, BC-35 | BC-3, BC-5 and BC-7; BC-12 and BC-14; BC-26 (a), BC-30, BC-31, WP-5 and WP-6; WP-13; BC-28b and BC-28c (b); BC-3, BC-11, BC-18, BC-24 and BC-28c |
| 5 | BC-33, BC-28e | BC-29, BC-32 and BC-15; BC-28a, S3 and BC-32 |

The cluster tasks run one at a time, and the recording ones land through the controller, as
the ledgers are append-only. BC-27 (a) records radosgw alone and goes first, since BC-26 (a)
waits for it. Then, each once its dependencies have merged: BC-8, BC-16, BC-22, BC-27 (b),
BC-28f, BC-34.

## Defects

The report's defect table, with each choice as this file rules it, plus defect 18, found while
drafting BC-29. Floors are v19.2.6 and v20.2.4 unless stated. "Unreproduced" means derived from
the source. The task that writes an entry runs its prior-art search, and the area's recording
task budgets the cluster reproduction.

| # | Defect | Class | Status | Upstream | rgw-go's choice (task) |
|---|---|---|---|---|---|
| 1 | A non-preflight request with an Origin no CORS rule allows is served; `generate_cors_headers` only omits the headers | S3-correctness | unreproduced; s3-tests expect the refusal | fixed on main by 4e056f98e78, 8d652cd24c1, 7f147f02058; in no tag | refuse with 403, as main (BC-4) |
| 2 | The rule chosen is the first whose origin matches, so a later rule that allows the request is never used ([rgw_cors.cc:186-194](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.cc#L186-L194)) | S3-correctness | unreproduced | fixed on main by cc1dcecee08 (PR #69190) | main's selection (BC-2) |
| 3 | A signed preflight's signature is never compared | quirk, as AWS checks none; its rate-limit charge is security | unreproduced | reported to security@ceph.io 2026-10-05 | take the skip; phase 3 must not charge OPTIONS CORS to the key's user (BC-5, B4) |
| 4 | PutBucketCors and PutPublicAccessBlock retries write back the attrs the request started with ([rgw_op.cc:6103-6107](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6103-L6107), [rgw_op.cc:8669-8673](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8669-L8673)) | data corruption (a lost update of shared bucket state) | unreproduced | none found; the registry's PutBucketPolicy entry | set only the op's own attr (BC-3, BC-18) |
| 5 | Retried bucket writes are not authorized again (registry) | security | unreproduced | registry | re-authorize each retry (BC-3, BC-11, BC-18, BC-24, BC-28c); retitle its exclusion once they land (BC-35) |
| 6 | GetPublicAccessBlock without a block sends a default document after the 404, past its Content-Length ([rgw_rest_s3.cc:4613-4624](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4613-L4624)) | S3-correctness (HTTP framing; can desynchronize a keep-alive connection); possibly security-relevant, conditional on radosgw keeping the connection open (rgw-bug-reproduction's triage, BC19-1) | unreproduced; BC-22 captures the bytes and whether the connection stays open | none found | the error document alone (BC-19) |
| 7 | A public-access block that does not decode is ignored (registry) | security, low | unreproduced | registry | phase 1 fails closed; Get answers 500 (BC-18) |
| 8 | POST object is never checked for a suspended user: the check precedes the form's authentication | security (fail-open) | unreproduced | none found; prior-art search pending | 403 UserSuspended; reported upstream once reproduced (BC-32) |
| 9 | POST object stores a public canned `acl` under BlockPublicAcls | security (an owner's block bypassed) | unreproduced | the registry's PutObject, CopyObject and CreateMultipartUpload entry, extended by BC-30's fix round; pending: sent to rgw-bug-reproduction | 403 AccessDenied by `acl.Policy.IsPublic` of the built ACL (BC-32) |
| 10 | POST object's body bytes are not counted in the usage log | S3-correctness (usage accounting is shared RADOS state) | reported upstream (tracker #76157) | fixed on main by 7d2484c45b4; in no tag | count them, as main (BC-33) |
| 11 | Ownership-controls requests fall through to ListObjects, CreateBucket and DeleteBucket; DELETE `?ownershipControls` deletes an empty bucket | S3-correctness, losing the bucket | unreproduced | the feature lands in v21.0.0 (b3733296c8e) | v21's feature (BC-28a to BC-28e, B7) |
| 12 | PutBucketEncryption stores any SSEAlgorithm, and a KMS key id with any algorithm | S3-correctness | unreproduced | none found | validate as AWS documents (BC-23, B5) |
| 13 | Squid's `is_public` counts Allow statements without a wildcard principal (registry) | S3-correctness (over-block) | unreproduced | fixed in v20.0.0 | S3's judgment (BC-20, B3) |
| 14 | `is_public` judges conditions against a fixed three-key environment (registry) | security | unreproduced | reported to security@ceph.io | S3's judgment (BC-20, B3) |
| 15 | PutBucketWebsite answers an unparseable body 400 InvalidArgument, where AWS answers MalformedXML | S3-correctness, minor | unreproduced | none | MalformedXML (BC-10, B2) |
| 16 | The block document lacks the S3 namespace; PolicyStatus and block booleans are lower-case | cosmetic (SDKs accept both) | from the source | none | keep radosgw's (BC-17, BC-21, B2) |
| 17 | POST's 201 Location under a configured domain has no scheme (registry's CompleteMultipartUpload entry, the same `compute_domain_uri`) | cosmetic | unreproduced | registry | as rgw-go's CompleteMultipartUpload decided (BC-33) |
| 18 | A POST form's part ends wherever `--` boundary appears, mid-line included, so file data holding it is cut there: stored truncated when the rest parses as parts, else refused ([rgw_rest.cc:1173-1220](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1173-L1220), [rgw_rest.cc:1173-1220](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1173-L1220)) | S3-correctness (an upload stored other than sent) | unreproduced, from the source; found while drafting this unit | none searched | RFC 2046's delimiter (BC-29, B11) |

Not defects, recorded so no task re-derives them: the head write applies a default retention
to POST objects as it does for PutObject; a redirect-all answers the code it stores when no
index suffix is stored, because `retarget` then fails at `get_effective_key`
([rgw_rest_s3.cc:5409-5414](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5409-L5414), [rgw_rest_s3.cc:5935-5940](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5935-L5940)) and the website
error handler's `should_redirect`, the first call, copies the stored code, which `apply_rule`
answers when it is above 0 ([rgw_website.cc:101-106](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L101-L106), [rgw_website.cc:101-106](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L101-L106);
[rgw_website.cc:61-62](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L61-L62), [rgw_website.cc:61-62](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L61-L62)), and
301 when it stores none, which is every redirect-all S3's PutBucketWebsite writes; with an
index suffix stored, `retarget`'s call writes 301 into the configuration first and the error
handler copies that; SSE-S3 and SSE-KMS fail on an unconfigured radosgw by design (the
key-management exclusion).
