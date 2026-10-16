# Phase 2 unit B: bucket configuration

Threat model: a client adversary at the security boundaries PP-14 names, which get adversarial
review: authentication (BC-5, BC-31), authorization (BC-4, BC-18, BC-28c, BC-28e, BC-32) and the
parsers of a client's documents (BC-2, BC-10, BC-17, BC-23, BC-28a, BC-29, BC-30); everything
else here, the configuration storage, the website renderer and the recording tasks, defends
against honest mistakes, and every write keeps the bucket instance radosgw-readable.

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
| B4 | A signed preflight's signature is not compared, as radosgw and AWS do (D18). The registry entry records that phase 3's rate limiter must not charge OPTIONS CORS to the key's user. | The presigned CORS tests need it; the rate-limit charge is the security half. |
| B5 | PutBucketEncryption is validated as AWS documents: SSEAlgorithm one of `AES256`, `aws:kms`, `aws:kms:dsse`, `aws:fsx`, `aws:backup`; KMSMasterKeyID only with `aws:kms` or `aws:kms:dsse`; 400 MalformedXML otherwise (D19). The stored form is unchanged. | AWS's API reference; a stored default neither gateway can serve fails a write on both, as on an unconfigured radosgw. |
| B6 | The six encryption-configuration s3-tests run through the T unit's include list (D20); BC-27 adds their lines. The eight ownership tests carry only `object_ownership`, so BC-28f lifts that marker. | The T unit owns the selection; each area owns its own lines. |
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
  `Redirect(conf meta.WebsiteConf, key string, status int, proto, host string) (url string, code int, ok bool)`.
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
| `internal/s3/headers.go`, `internal/s3/errors.go` | BC-7 (`SetCommonHeaders`), BC-15 (the HTML error variant) | - | one owner each |
| `internal/op/op.go` | BC-4 (one call into `corsmatch.go`) | - | single owner |
| `internal/op/request.go` | BC-13 (`Website`, `Redirect`), BC-26 (`CryptAttrs`) | versioning | additive fields, each task's in its own PR |
| `internal/op/getobject.go` | BC-14 (one hook field, one call; the body in `website_get.go`) | versioning, WP-12 | line-local |
| `internal/op/{putobject,initmultipart,uploadpart}.go` | BC-26 | WP-6, V11, L5b | BC-26 after WP-6 (index Order 3) |
| `internal/op/copyobject.go` | BC-26 (b) | V11 | never in parallel |
| `internal/s3/{putobject,copyobject}.go`, `internal/s3/multipart.go` | BC-26; BC-13 (b) (`completeLocation`) | WP-7, WP-13, versioning, L5b, C8 | One call line per site, so whichever lands first, the later rebases; BC-26's op-file changes keep their order after WP-6 and V11, which share function bodies (index Order) |
| `internal/authz/{env,load,write,evaluator}.go` | BC-26 (`cryptKeys` in `env.go`), BC-28e | S3 (the `IsPublic` callers) | BC-28e after S3 |
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
- **Consumes:** BC-2's `Decode`, `Match`. **Produces:** `AttrCORS` (`user.rgw.cors`);
  `GetBucketCORS{Config}`, `PutBucketCORS{Params}` (a body hook, as PutBucketTagging's),
  `DeleteBucketCORS`, and `OptionsCORS` with its results: the rule, the origin, the method and
  the allowed requested headers.
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
     [rgw_op.cc:6826-6874](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6826-L6874)). The op types and masks
     are radosgw's: get and options read, put and delete write
     ([rgw_op.h:1697-1764](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1697-L1764), [rgw_op.h:1850-1920](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1850-L1920)).
- **Specs:**
  - each op's error order with counterfeiter fakes;
  - a PutBucketCors that loses a race to a PutBucketAcl keeps the winner's ACL;
  - a retry runs VerifyPermission again and writes nothing for a requester the new ACL refuses;
  - OptionsCORS on a bucket whose rules allow the origin but not the method.
- **Exclusions and registry:** extends "A retried PutBucketPolicy keeps what the write it lost
  to changed" and its registry entry with PutBucketCors (defect 4, data corruption,
  unreproduced); extends "A PutBucketAcl that loses a race is retried, and every retried ACL,
  policy or tagging write is authorized again" with the CORS writes (defect 5).
- **Depends on:** BC-2. **PR:** one. **Brief:** about 180 lines.

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
- **Consumes:** BC-3's ops; phase 1's `readParamBody` (`internal/s3/bucket.go:135`) and
  `writeXML` (`internal/s3/xml.go:33`).
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
  3. **delete_cors:** 204, with or without a configuration
     ([rgw_rest_s3.cc:3843-3852](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3843-L3852), [rgw_rest_s3.cc:4294-4303](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4294-L4303)).
  4. **options_cors:** 200 with BC-2's preflight headers, else the op's 403 or 400
     ([rgw_rest_s3.cc:3854-3875](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3854-L3875),
     [rgw_rest_s3.cc:4305-4326](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4305-L4326)).
- **Specs:** status, headers and body of each route from test_set_cors, test_cors_header_option,
  test_cors_origin_wildcard and the presigned preflight cases.
- **Exclusions and registry:** none.
- **Depends on:** S2, BC-3, BC-5 (the presigned preflight specs). **PR:** one. **Brief:** about
  150 lines.

### Task BC-7: CORS headers on responses

- **Layer:** handler. **Files:** modify `internal/s3/headers.go` (`SetCommonHeaders`).
  **Cluster:** no.
- **Consumes:** BC-2's `Decode`, `Match`, `ResponseHeaders`; BC-3's `AttrCORS`.
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
- **Specs:** GET object 200 and 404 with the headers; an authentication failure without them;
  a presigned request on a wildcard rule gets `*`; a method the rule does not allow; a
  PutBucketEncryption response with the headers.
- **Exclusions and registry:** new exclusion "Every S3 response carries CORS headers once its
  bucket is loaded", listing radosgw's omissions.
- **Depends on:** BC-2, BC-3. **PR:** one. **Brief:** about 120 lines.

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
     entry's prior-art search.
- **Specs:** the gate cases above, labelled `integration, rgw-go, phase2, cors`.
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
  `EffectiveKey`, `Redirect` (signatures in Seams).
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
     outside 400-599, and ReplaceKeyPrefixWith with ReplaceKeyWith are 400 InvalidArgument;
     RedirectAllRequestsTo makes the rest ignored
     ([rgw_website.cc:214-341](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L214-L341), [rgw_website.cc:214-341](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L214-L341)).
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
- **Specs:** a table from each cited function; the redirect-code and error-code bounds; the
  routing-rule limit with its message; the unparseable body's MalformedXML.
- **Exclusions and registry:** new exclusion for the MalformedXML code (B2); new registry
  entry for defect 15 (S3-correctness, minor, unreproduced).
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
- **Consumes:** BC-10's `EffectiveKey` and `Redirect`; BC-13's `Request.Website`.
  **Produces:** the retarget hook GetObject calls between its bucket load and its object read.
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
     effective key is read as a GetObject.
  4. An object holding `user.rgw.x-amz-website-redirect-location` is 301 with that Location and
     no body ([rgw_rest_s3.cc:274-292](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L274-L292),
     [rgw_rest_s3.cc:271-289](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L271-L289)).
- **Specs:** each effective-key case; a directory object; a redirect-all; a routing rule with
  and without a code; an object with a redirect attr; the original key kept for BC-15.
- **Exclusions and registry:** none.
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
  4. WebsiteRedirect carries Location.
- **Specs:** status, headers and body for each path; an error document that is itself missing
  falls back to the HTML page; a conditional GET's error document served unconditionally; PUT
  on a website host 405 in HTML.
- **Exclusions and registry:** none.
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

- **Layer:** op. **Files:** create `internal/op/publicaccess.go`. **Cluster:** no.
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
     no change to authz.
- **Specs:** each op's error order with fakes; a lost race keeps the winner's ACL; a retry
  re-authorized; a block written here, then enforced by phase 1's checks: BlockPublicAcls on
  PutBucketAcl, IgnorePublicAcls on an anonymous GET, and BlockPublicPolicy and
  RestrictPublicBuckets judged by S3's `IsPublic`.
- **Exclusions and registry:** extends defect 4's exclusion and registry entry with
  PutPublicAccessBlock; extends defect 5's exclusion with the block writes; adds rgw-go's Get
  answer to the registry entry for defect 7.
- **Depends on:** BC-17, S3 (the enforcement specs). **PR:** one. **Brief:** about 120 lines.

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
  2. Gate case 3: each gateway's PutPublicAccessBlock read back and enforced by the other
     (BlockPublicAcls on PutBucketAcl, IgnorePublicAcls on an anonymous GET, BlockPublicPolicy
     on PutBucketPolicy, RestrictPublicBuckets on Tentacle); the attr bytes equal.
  3. Gate case 5: the same IsPublic from both for public-read ACLs, a public policy and a
     private bucket, with a known-differences line wherever S3's judgment departs from a floor's.
  4. Reproduce defect 6 from the raw response bytes on both releases.
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
  both ways; each validation refusal; an s3-tests addition for an unknown SSEAlgorithm, to the
  `rgw-go-s3tests` branch.
- **Exclusions and registry:** rewrites item 2 of "Key management: SSE-KMS and SSE-S3 with
  every backend", which says the configuration round-trips; new registry entry for defect 12
  (S3-correctness, unreproduced).
- **Depends on:** nothing. **PR:** one. **Brief:** about 150 lines.

### Task BC-24: Encryption configuration ops

- **Layer:** op. **Files:** create `internal/op/bucketencryption.go`. **Cluster:** no.
- **Consumes:** BC-23; `op.AttrBucketEncryption` (`internal/op/putobject.go:17-19`).
  **Produces:** `GetBucketEncryption{Config}`, `PutBucketEncryption{Params}`,
  `DeleteBucketEncryption`, and the key-id attr name `user.rgw.sse-s3.key-id`.
- **Behaviour:**
  1. **Put.** Action s3:PutBucketEncryption with no tag loading; it stores the validated
     configuration in `user.rgw.sse-s3.policy` alone on each try
     ([rgw_op.cc:8757-8812](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8757-L8812),
     [rgw_op.cc:9725-9780](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9725-L9780)).
  2. **Get.** Action s3:GetBucketEncryption; no attr is ErrNoSuchBucketEncryption with "The
     server side encryption configuration was not found"; an undecodable one is 500
     ([rgw_op.cc:8814-8841](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8814-L8841),
     [rgw_op.cc:9782-9809](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9782-L9809)). The tags differ
     only in the log level.
  3. **Delete.** Authorized as s3:PutBucketEncryption; it removes `user.rgw.sse-s3.policy` and
     `user.rgw.sse-s3.key-id`, which only a radosgw with an SSE-S3 backend writes; success when
     absent ([rgw_op.cc:8843-8867](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8843-L8867),
     [rgw_op.cc:9811-9835](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9811-L9835)).
- **Specs:** each op's error order with fakes; Delete removing both attrs; a retry
  re-authorized.
- **Exclusions and registry:** extends defect 5's exclusion with the encryption writes.
- **Depends on:** BC-23. **PR:** one. **Brief:** about 110 lines.

### Task BC-25: Encryption configuration routes

- **Layer:** handler. **Files:** S2's `internal/s3/encryption.go`. **Cluster:** no.
- **Consumes:** BC-23's XML, BC-24's ops.
- **Behaviour:** PUT 200 with an empty body; GET the document in the S3 namespace; DELETE 204
  ([rgw_rest_s3.cc:3877-3915](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3877-L3915),
  [rgw_rest_s3.cc:4328-4366](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4328-L4366)).
- **Specs:** each status, header and body, from the six configuration s3-tests.
- **Exclusions and registry:** none.
- **Depends on:** S2, BC-24. **PR:** one. **Brief:** about 80 lines.

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
     [rgw_rest_s3.cc:4454](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4454)):
     - SSE-C with SSE-KMS or SSE-S3 headers is 400 InvalidArgument, before authorization;
     - SSE-C wins; otherwise the stored algorithm and key id fill what the request names none
       of, KMS attributes without an algorithm mean `aws:kms`, and KMS attributes with another
       algorithm are 400;
     - the filled attributes become `Request.CryptAttrs`, so the policy environment sees the
       default before authorization;
     - a stored configuration that does not decode is 400 InvalidArgument "Server side error -
       can't decode bucket_encryption_conf", after authorization (B16), with the policy
       environment built from the request alone.
  2. **The unconfigured `rgw_s3_prepare_encrypt`** ([rgw_crypt.cc:1030-1301](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_crypt.cc#L1030-L1301),
     [rgw_crypt.cc:1045-1315](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1045-L1315)), after authorization:
     - with `rgw_crypt_require_ssl` (default true) over plain HTTP, 400 InvalidRequest
       ([rgw.yaml.in:2823-2827](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L2823-L2827), [rgw.yaml.in:2955-2959](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2955-L2959));
     - `aws:kms` without a key id, and an algorithm other than `aws:kms` and `AES256`, 400
       InvalidArgument;
     - otherwise the answer BC-27 records from a running radosgw, which source reading puts at
       400 InvalidArgument (the empty Barbican and Vault addresses,
       [rgw_kms.cc:919-979](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_kms.cc#L919-L979), [rgw_kms.cc:917-977](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_kms.cc#L917-L977),
       [rgw_kms.cc:255-271](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_kms.cc#L255-L271), [rgw_kms.cc:255-271](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_kms.cc#L255-L271)).
  3. SSE-C keeps phase 1's 501 until phase 4. A configuration naming no algorithm refuses
     nothing (B15).
  4. CopyObject and UploadPart are not among radosgw's callers above. PR (b) first reads how
     each chooses its destination's encryption at both tags and cites it in its notes, then
     replaces phase 1's 501 with that behaviour.
- **Specs:** every refusal of the two functions; TLS and plain HTTP; the policy keys with and
  without a default; a default without an algorithm; an undecodable default after a 403.
- **Exclusions and registry:** retires the SSE-S3 and SSE-KMS half of "Writes that ask for
  encryption answer 501, SSE-S3 and SSE-KMS until phase 2 and SSE-C until phase 4." and "A
  PUT's encryption condition keys leave out the bucket's default encryption."; extends "PutObject
  and CopyObject authorize a request before they read what the store holds for it" with the
  undecodable default (B16).
- **Depends on:** BC-23; BC-27 (a), whose recorded statuses the no-backend specs pin; the
  write-path unit on the same files: PR (a) after WP-6 and WP-7, PR (b) after WP-6, WP-7 and
  WP-13 (index Order 3), never in parallel with V11, L5b or C8 on a file they share.
- **PR split:** (a) the helper, `CryptAttrs` and `cryptKeys`, with PutObject and
  CreateMultipartUpload; (b) CopyObject and UploadPart. **Brief:** about 220 lines.

### Task BC-27: Encryption recording and gate

- **Layer:** integration. **Files:** the T unit's `hack/s3tests/include-phase2.txt` (this
  unit's six lines), both ledgers; new `test/integration/phase2_encryption_test.go`.
  **Cluster:** yes.
- **Behaviour:**
  1. **(a) radosgw's write answers.** On an unconfigured zone, over TLS and over plain HTTP,
     record radosgw's status and error code for PutObject, CreateMultipartUpload and POST
     object into buckets whose default is `AES256` and `aws:kms`, and for the same writes asking
     for SSE-S3 and SSE-KMS by header. The table goes into the registry's key-management notes;
     BC-26's specs pin it. This half needs nothing from rgw-go.
  2. **(b) the configuration tests.** Add test_put/get/delete_bucket_encryption_s3 and _kms
     (`test_s3.py:15343-15435` at the s3-tests pin) to the include list (B6) and record both
     ledgers.
  3. **(b) gate case 4:** each gateway writes `AES256` and `aws:kms` configurations the other
     reads, the attr bytes equal and equal to the corpus encoding; rgw-go's write answers equal
     the table of (a).
- **Specs:** the gate cases above, labelled `integration, rgw-go, phase2, encryption`.
- **Depends on:** (a) nothing; (b) BC-23 to BC-26 and WP-23 (the include list).
  **PR split:** (a) the recording; (b) the include lines, the ledgers and the gate; each landed
  by the controller. **Brief:** about 70 lines.

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
- **Exclusions and registry:** none.
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
  served as v21 serves them".
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
  carries the attr through its attr merges.
- **Depends on:** BC-28a. **PR split:** (a) the three ops; (b) the ACL refusals and
  CreateBucket, after L2's PR (b) on `bucketops.go`. **Brief:** about 200 lines.

### Task BC-28d: Ownership routes

- **Layer:** handler. **Files:** S2's `internal/s3/ownership.go`; modify the CreateBucket
  handler in `internal/s3/bucket.go` (one header read). **Cluster:** no.
- **Consumes:** BC-28a's XML, BC-28c's ops and `CreateBucket.ObjectOwnership`.
- **Behaviour** ([rgw_rest_s3.cc:4553-4618](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4553-L4618)):
  1. PUT reads the body up to `rgw_max_put_param_size`; a parse failure, no `OwnershipControls`
     or a refused rule is 400 MalformedXML; 200 with an empty body.
  2. GET 200 with the document; DELETE 204.
  3. CreateBucket's `x-amz-object-ownership` header becomes the op's value; a value v21 does not
     parse is 400 InvalidArgument with its message ([rgw_rest_s3.cc:2809-2816](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L2809-L2816)).
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
     compare case-insensitively ([rgw_rest.h:247-249](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.h#L247-L249), [rgw_rest.h:247-249](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.h#L247-L249)).
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
  - **Review Focus 5, a boundary string inside the file data:** file data holding `--` boundary
    mid-line, and the boundary text without its leading CRLF, reads back byte-identical;
  - **Review Focus 5, a file larger than memory:** a generated multi-GiB file part, never held
    in memory, read through the file reader into a discarding sink, with the heap's growth
    under a fixed multiple of the read size;
  - an s3-tests addition for the boundary case, to the `rgw-go-s3tests` branch.
- **Exclusions and registry:** new exclusion for the delimiter rule; new registry entry for
  defect 18 (S3-correctness: an upload cut at the string, stored truncated or refused;
  unreproduced, from the source).
- **Depends on:** nothing. **PR:** one. **Brief:** about 200 lines.

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
  3. `key` is required (400 "Key not specified"); its first `${filename}` is replaced
     ([rgw_rest_s3.cc:2889-2902](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2889-L2902),
     [rgw_rest_s3.cc:3015-3028](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3015-L3028)); an empty
     result is 400 "Empty object name". Then `key` enters the environment as B12 rules; radosgw
     enters the replaced key ([rgw_rest_s3.cc:2999-3006](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2999-L3006),
     [rgw_rest_s3.cc:3137-3144](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3137-L3144)).
  4. `Content-Type` enters the environment when set; `x-amz-storage-class` must be a valid
     placement ("The storage class you specified is not valid"); `x-amz-meta-*` and
     `x-amz-website-redirect-location` become NUL-terminated attrs.
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
     refuses one (`internal/op/putobject.go:198-211`; B11, defect 9).
  10. The quota check; Content-MD5 (400 InvalidDigest or BadDigest); the versioned write
     (`VersionForWrite`, or phase 1's 501 until V11); BC-26's encryption; the checksum field
     through WP-6's parser, D21's refusals included; `ObjectStore.PutObject` streaming the file
     with the policy's bounds (400 EntityTooLarge over the maximum, EntityTooSmall under the
     minimum); the attrs ETag, ACL, Content-Type (NUL-terminated), metas, the redirect and tags.
     Default retention comes from the head write, as for PutObject
     ([rgw_rados.cc:3211-3215](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3211-L3215),
     [rgw_rados.cc:3308-3312](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3308-L3312)). **Differs:** v20.2.4 adds the
     streaming checksum and `user.rgw.cksum`; rgw-go serves it on both releases (D22).
- **Specs:**
  - the error order across Init, VerifyPermission and Execute, with counterfeiter fakes;
  - each length bound; each attr; a suspended user; a public canned ACL under a block;
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
  CreateMultipartUpload and UploadPart" and the registry's matching entry with POST object
  (defect 9); retires the last sentence of "PutObject and CopyObject authorize a request before
  they read what the store holds for it", which says rgw-go serves no POST object; an exclusion
  for B12 if AWS's page departs from radosgw.
- **Depends on:** BC-26 (a), BC-30, BC-31, WP-5, WP-6. **PR split:** (a) Init and
  VerifyPermission; (b) Execute. **Brief:** about 240 lines.

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
     204 with the ETag header; radosgw's messages go into the error documents.
  3. The 201's Location takes its domain as rgw-go's CompleteMultipartUpload does
     (`internal/s3/multipart.go:341-360`; defect 17).
  4. **Differs from both floors by design (B11):** the usage log counts the body, as main does
     ([rgw_rest.cc:1236-1238](https://github.com/ceph/ceph/blob/7d2484c45b4/src/rgw/rgw_rest.cc#L1236-L1238)); the
     byte counter, today PutObject's alone (`internal/s3/handler.go:473-486`), covers post_obj.
- **Specs:** each status and header; `${filename}` end to end; a tenant's redirect; the usage
  entry's bytes received.
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

## Order

Prerequisites from other units: S2 for every handler task; S3 for BC-18, BC-20 and BC-28e;
WP-1 for BC-30; WP-5 and WP-6 for BC-32; WP-6, WP-7 and WP-13 for BC-26 (Hot files); WP-23 for
BC-27 (b); L2's PR (b) for BC-28c (b) and BC-28d. Within the unit:

| Wave | Tasks, in parallel | Each waits for |
|---|---|---|
| 1 | BC-2, BC-5, BC-9, BC-13, BC-17, BC-23, BC-28a, BC-28b, BC-29, BC-31; BC-20; BC-30 | nothing in the unit; BC-20 S3; BC-30 WP-1 |
| 2 | BC-3, BC-10, BC-11, BC-18, BC-21, BC-24, BC-28c (a) | BC-2; BC-9; BC-9; BC-17 and S3; BC-20; BC-23; BC-28a |
| 3 | BC-4, BC-6, BC-7, BC-12, BC-14, BC-19, BC-25, BC-26 (a), BC-28c (b) | BC-3; BC-3 and BC-5; BC-3; BC-10 and BC-11; BC-10 and BC-13; BC-18; BC-24; BC-23, BC-27 (a), WP-6 and WP-7; BC-28c (a) and L2 (b) |
| 4 | BC-15, BC-32, BC-26 (b), BC-28d | BC-12 and BC-14; BC-26 (a), BC-30, BC-31, WP-5 and WP-6; WP-13; BC-28b and BC-28c (b) |
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
| 5 | Retried bucket writes are not authorized again (registry) | security | unreproduced | registry | re-authorize each retry (BC-3, BC-11, BC-18, BC-24, BC-28c) |
| 6 | GetPublicAccessBlock without a block sends a default document after the 404, past its Content-Length ([rgw_rest_s3.cc:4613-4624](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4613-L4624)) | S3-correctness (HTTP framing; can desynchronize a keep-alive connection) | unreproduced; BC-22 captures the bytes | none found | the error document alone (BC-19) |
| 7 | A public-access block that does not decode is ignored (registry) | security, low | unreproduced | registry | phase 1 fails closed; Get answers 500 (BC-18) |
| 8 | POST object is never checked for a suspended user: the check precedes the form's authentication | security (fail-open) | unreproduced | none found; prior-art search pending | 403 UserSuspended; reported upstream once reproduced (BC-32) |
| 9 | POST object stores a public canned `acl` under BlockPublicAcls | security (an owner's block bypassed) | unreproduced | extends the registry's PutObject, CopyObject and CreateMultipartUpload entry | 403 AccessDenied (BC-32) |
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
to POST objects as it does for PutObject; `should_redirect`'s write of 301 into the
configuration leaves every redirect-all at 301 anyway; SSE-S3 and SSE-KMS fail on an
unconfigured radosgw by design (the key-management exclusion).
