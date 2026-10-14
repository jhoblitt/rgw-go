# Phase 2 research: `bucketconfig`

Area: CORS, static website, bucket encryption configuration, ownership controls, public access
block, policy status, POST object.

Sources: rgw-go at origin/main 0239625 (read from a `git archive` snapshot, since the
phase-1-exec worktree's HEAD, af535e4, is behind main); ceph at v19.2.6 and v20.2.4, and
origin/main 292995374d9 where upstream has fixed a floor defect (the local `main` ref,
7ed73efc1be, is stale); s3-tests at the PIN f6f2529dd9ef; AWS S3 documentation where the
owner's rule of 2026-10-13 makes AWS the reference. Every fact below is in
`research-bucketconfig.notes.md` with its source. Citations to upstream main use the commit as
the ref (`@4e056f98e78`), which `hack/citecheck` resolves like a tag.

**The owner's rule of 2026-10-13 governs every choice in this report.** Compatibility means RADOS
state in both directions. Where radosgw's S3 behaviour is wrong, rgw-go does the correct S3
thing, usually AWS's documented behaviour, as long as the stored form, the xattr or the
RGWBucketInfo field, stays the one radosgw writes and reads. rgw-go prevents corruption of
shared data even where radosgw allows it. Each difference is an exclusion.

## 1. Scope

The spec's phase 2 items (§9) in this area, in its words: "CORS with preflight, static website,
the encryption configuration ops behaving as an unconfigured radosgw, ownership controls,
public access block, policy status, POST object".

| Item | S3 surface | Route names (rgw-go dispatch) | Stored form |
|---|---|---|---|
| CORS | GetBucketCors, PutBucketCors, DeleteBucketCors (`?cors`); the preflight `OPTIONS` at bucket and object scope; the `Access-Control-*` and `Vary` headers on ordinary responses, presigned ones included; main's refusal of a non-preflight request whose Origin no rule allows | get_cors, put_cors, delete_cors, options_cors | bucket attr `user.rgw.cors`, RGWCORSConfiguration v1 |
| Static website | GetBucketWebsite, PutBucketWebsite, DeleteBucketWebsite (`?website`, gated by `rgw_enable_static_website`); the website endpoint: `s3website` in `rgw_enable_apis`, `rgw_dns_s3website_name` and the zonegroup's `hostnames_s3website`, index document, `web_dir`, error document, routing rules, RedirectAllRequestsTo, `x-amz-website-redirect-location`, HTML errors | get_bucket_website, set_bucket_website, delete_bucket_website; website-mode get_obj | `RGWBucketInfo.website_conf` and `has_website` (a field of the bucket instance, not an attr), RGWBucketWebsiteConf v2 |
| Encryption configuration | GetBucketEncryption, PutBucketEncryption, DeleteBucketEncryption (`?encryption`); the bucket default's effect on PutObject, POST object and CreateMultipartUpload (`get_encryption_defaults`) | get/put/delete_bucket_encryption | bucket attr `user.rgw.sse-s3.policy`, RGWBucketEncryptionConfig v1; Delete also removes `user.rgw.sse-s3.key-id` |
| Ownership controls | Get/Put/DeleteBucketOwnershipControls (`?ownershipControls`); CreateBucket's `x-amz-object-ownership` | none: neither floor has the feature, and the request falls through to list_bucket, create_bucket or delete_bucket | none at the floors; `user.rgw.ownership-controls` from v21.0.0 |
| Public access block | Get/Put/DeletePublicAccessBlock (`?publicAccessBlock`); its enforcement, which phase 1 already applies to a stored block | get/put/delete_bucket_public_access_block | bucket attr `user.rgw.public-access`, PublicAccessBlockConfiguration v1 |
| Policy status | GetBucketPolicyStatus (`?policyStatus`) | get_bucket_policy_status | none (computed) |
| POST object | browser upload: `POST /bucket` and `POST /bucket/key` with `multipart/form-data`; the policy document; SigV2 (`AWSAccessKeyId`, `signature`) and SigV4 (`x-amz-algorithm`, `x-amz-credential`, `x-amz-date`, `x-amz-signature`) form credentials; `x-amz-security-token`; conditions `eq`, `starts-with`, `content-length-range`, simple checks; `key` with `${filename}`; `acl`, `Content-Type`, `x-amz-meta-*`, `x-amz-storage-class`, `x-amz-website-redirect-location`, `tagging`, `x-amz-server-side-encryption*`, Tentacle's `x-amz-checksum-*`; `success_action_status`, `success_action_redirect` | post_obj (bucket and object scope) | the object, written as PutObject writes it |

No admin endpoint is in this area. The admin metadata API's `bucket.instance` get and put must
render and accept `website_conf` once it is modelled: phase 0 answers 501 for a bucket that has
one.

## 2. radosgw's behaviour

Common to every bucket op here: `init_permissions` loads the bucket and its attrs (a missing
bucket is NoSuchBucket before any permission check), each op authorizes one action against the
bucket through `verify_bucket_permission` (the ACL, the bucket policy, identity policies, the
public-access block and, where a policy names `s3:ResourceTag`, the bucket's tags), the system
or admin override applies, and a write forwards to the metadata master, a no-op on one zone.
Attr writes go through `retry_raced_bucket_write`, which on ECANCELED re-reads the bucket and
retries up to fifteen times. In RADOS terms each write is one guarded rewrite of the bucket
instance object `.bucket.meta.<name>:<id>` (cls_version) with a cache notify; no cls_rgw call
and no index object is touched. rgw-go's `op.RetryRacedBucketWrite`, `retryBucketWrite` and
`BucketStore.PutBucketAttrs`/`PutBucketInfo` already do exactly this
(`internal/op/bucketsubres.go:56-90`, `internal/op/bucket.go:114-117` @0239625). Unless a line
says otherwise, the code is identical at both tags apart from the forward call's added `s->err`
argument at v20.2.4.

### 2.1 CORS

- **GetBucketCors.** Action s3:GetBucketCORS, bucket tags added when a policy names
  s3:ResourceTag (`src/rgw/rgw_op.cc:6046-6057@v19.2.6#RGWGetCORS::verify_permission`,
  `src/rgw/rgw_op.cc:6716-6727@v20.2.4#RGWGetCORS::verify_permission`). No `user.rgw.cors` attr is 404 NoSuchCORSConfiguration
  (ERR_NO_CORS_FOUND, errno 2216); an attr that does not decode is EIO, 500
  (`src/rgw/rgw_op.cc:1483-1512@v19.2.6#RGWOp::read_bucket_cors`,
  `src/rgw/rgw_op.cc:6059-6070@v19.2.6#RGWGetCORS::execute`). The 200 body is `CORSConfiguration` in the S3
  namespace (`src/rgw/rgw_rest_s3.cc:3719-3740@v19.2.6#RGWGetCORS_ObjStore_S3::send_response`,
  `src/rgw/rgw_rest_s3.cc:4200-4221@v20.2.4#RGWGetCORS_ObjStore_S3::send_response`), each rule rendered with ID when non-empty, AllowedMethod in the fixed
  order GET, PUT, DELETE, HEAD, POST, COPY, AllowedOrigin and AllowedHeader sorted (they are
  `std::set`s), MaxAgeSeconds when valid, ExposeHeader in stored order
  (`src/rgw/rgw_cors_s3.cc:32-74@v19.2.6#RGWCORSRule_S3::to_xml`).
- **PutBucketCors.** Action s3:PutBucketCORS (`src/rgw/rgw_op.cc:6072-6083@v19.2.6`). The body
  is read up to `rgw_max_put_param_size`; a parse failure or no `CORSConfiguration` root is 400
  MalformedXML; more rules than `rgw_cors_rules_max_num` (default 100, a negative value meaning
  100) is 400 InvalidRequest "The number of CORS rules should not exceed allowed limit of N
  rules." (`src/rgw/rgw_rest_s3.cc:3742-3802@v19.2.6#RGWPutCORS_ObjStore_S3::get_params`).
  A rule is refused, as MalformedXML, for an AllowedMethod other than GET, POST, DELETE, HEAD,
  PUT or COPY (case-insensitive), an ID over 255 bytes, no AllowedOrigin, an AllowedOrigin or
  AllowedHeader that is empty or holds more than one `*`, or a MaxAgeSeconds with trailing
  junk; a MaxAgeSeconds of 2^32 or more is stored as "none"; a rule with no AllowedMethod is
  accepted (`src/rgw/rgw_cors_s3.cc:76-164@v19.2.6#RGWCORSRule_S3::xml_end`); a configuration
  needs one rule (`:177-188`). The encoded configuration replaces the attr
  (`src/rgw/rgw_op.cc:6085-6105@v19.2.6#RGWPutCORS::execute`), 200 with an empty body
  (`src/rgw/rgw_rest_s3.cc:3804-3811@v19.2.6`). Encoding:
  `src/rgw/rgw_cors.h:63-82@v19.2.6` (RGWCORSRule, ENCODE_START(1,1): max_age u32,
  allowed_methods u8, id, allowed_hdrs set, allowed_origins set, exposable_hdrs list) and
  `:102-111` (RGWCORSConfiguration (1,1): rules list). Byte-identical files at both tags.
- **DeleteBucketCors.** Action s3:PutBucketCORS, "No separate delete permission"
  (`src/rgw/rgw_op.cc:6107-6119@v19.2.6`); removes the attr; a bucket without one answers 204
  too (`src/rgw/rgw_op.cc:6121-6150@v19.2.6#RGWDeleteCORS::execute`,
  `src/rgw/rgw_rest_s3.cc:3813-3822@v19.2.6`).
- **Preflight, OPTIONS on a bucket or an object.** Authentication first: a signed OPTIONS is
  signed over its Access-Control-Request-Method, and a missing one, or one other than exactly
  GET, POST, PUT, DELETE or HEAD, is 400 InvalidArgument before the key is looked up, for SigV4
  at both tags (`src/rgw/rgw_auth_s3.cc:705-732@v19.2.6#get_v4_canonical_method`) and for SigV2
  only at v20.2.4 (`src/rgw/rgw_auth_s3.cc:1749-1776@v20.2.4#get_canonical_method`, called at
  `src/rgw/rgw_auth_s3.cc:258@v20.2.4`). Once the key's user and account load and the key is in the user's record,
  the signature is not compared (`src/rgw/rgw_rest_s3.cc:6354-6360@v19.2.6`,
  `src/rgw/rgw_rest_s3.cc:6925-6931@v20.2.4`). The op checks no permission
  (`src/rgw/rgw_op.h:1752@v19.2.6`). Execute, in order: an undecodable CORS attr is 500; no
  Origin is 400 InvalidArgument; no Access-Control-Request-Method is 400 InvalidArgument; no
  CORS configuration is 403 AccessDenied; then the first rule whose origins match the Origin
  (case-sensitive, `*` wildcards) must allow the method and every requested header
  (lower-cased), else 403 (`src/rgw/rgw_op.cc:6156-6204@v19.2.6#RGWOptionsCORS::execute`,
  `src/rgw/rgw_rest_s3.cc:3824-3845@v19.2.6#RGWOptionsCORS_ObjStore_S3::send_response`). A 200
  carries Access-Control-Allow-Origin (the Origin as sent), Vary: Origin unless the origin is
  `*`, Access-Control-Allow-Methods (the requested method), Access-Control-Allow-Headers (the
  requested headers the rule allows, comma-joined), Access-Control-Expose-Headers (newlines
  escaped) and Access-Control-Max-Age when set
  (`src/rgw/rgw_rest.cc:526-554@v19.2.6#dump_access_control`).
- **Headers on ordinary responses.** `end_header` adds the same headers when the op is passed
  to it (`src/rgw/rgw_rest.cc:589-655@v19.2.6#end_header`, the call at `:604`), as computed by
  `src/rgw/rgw_op.cc:1541-1600@v19.2.6#RGWOp::generate_cors_headers`: an Origin header is
  needed and the bucket attrs must hold a configuration; the first rule matching the Origin is
  taken; Access-Control-Allow-Origin is `*` when the request has no Authorization header and
  the rule's origins include `*` (so every presigned request matching a wildcard rule gets
  `*`); the method is Access-Control-Request-Method if sent, else the request's own, and the
  rule must allow it. At v19.2.6, 50 `end_header` calls in `rgw_rest_s3.cc` pass the op and
  15 do not, among them ListBuckets, CreateBucket, the four CORS ops, PutBucketEncryption,
  DeleteBucketEncryption, PutBucketPublicAccessBlock and the object-lock and request-payment
  PUTs; v20.2.4 has 52 and 15 (a scan of every `end_header` call, its list in the notes). Errors from
  `abort_early` pass the op (`src/rgw/rgw_rest.cc:676-743@v19.2.6#abort_early`), so an error
  after `init_permissions` carries the headers when the Origin matches.
- **Phase 1 interaction.** The CORS ops are ordinary bucket ops for ACL, policy and tag
  authorization; the auth change touches phase 1's verifier (§3).

### 2.2 Static website

- **GetBucketWebsite.** Action s3:GetBucketWebsite, tags as above
  (`src/rgw/rgw_op.cc:2862-2873@v19.2.6`, `src/rgw/rgw_op.cc:3094-3105@v20.2.4`). No `has_website` is 404
  NoSuchWebsiteConfiguration (`src/rgw/rgw_op.cc:2880-2885@v19.2.6#RGWGetBucketWebsite::execute`).
  The body is `WebsiteConfiguration` in the S3 namespace holding RedirectAllRequestsTo
  (HostName, then Protocol when set), IndexDocument/Suffix, ErrorDocument/Key and RoutingRules,
  each only when set (`src/rgw/rgw_rest_s3.cc:2357-2375@v19.2.6`,
  `src/rgw/rgw_website.cc:214-316@v19.2.6`).
- **PutBucketWebsite.** Action s3:PutBucketWebsite (`src/rgw/rgw_op.cc:2887-2898@v19.2.6`).
  Validation (`src/rgw/rgw_rest_s3.cc:2271-2337@v19.2.6#RGWSetBucketWebsite_ObjStore_S3::get_params`,
  `src/rgw/rgw_rest_s3.cc:2374-2440@v20.2.4#RGWSetBucketWebsite_ObjStore_S3::get_params`): a body that does not parse, or lacks the mandatory
  `WebsiteConfiguration`, is 400 InvalidArgument (not MalformedXML); RedirectAllRequestsTo
  without HostName, neither RedirectAllRequestsTo nor IndexDocument, or an empty Suffix are each
  400 InvalidArgument with their own message; more routing rules than
  `rgw_website_routing_rules_max_num` (default 50) is 400 InvalidRequest. A HttpRedirectCode
  outside 301-399, a HttpErrorCodeReturnedEquals outside 400-599, or both ReplaceKeyPrefixWith
  and ReplaceKeyWith are decode errors, so InvalidArgument
  (`src/rgw/rgw_website.cc:235-275@v19.2.6`). When RedirectAllRequestsTo is present the rest
  is ignored (`:323-341`). The configuration is a field of the bucket instance: Execute sets
  `has_website` and `website_conf` and rewrites the instance with `put_info`
  (`src/rgw/rgw_op.cc:2905-2936@v19.2.6#RGWSetBucketWebsite::execute`), 200 with the op passed
  to `end_header`.
- **DeleteBucketWebsite.** Action s3:DeleteBucketWebsite; clears both fields and rewrites the
  instance; 204, with nothing to delete as well (`src/rgw/rgw_op.cc:2938-2981@v19.2.6`,
  `src/rgw/rgw_rest_s3.cc:2347-2355@v19.2.6`).
- **The option gate.** With `rgw_enable_static_website` false (the default,
  `src/common/options/rgw.yaml.in:2430-2437@v19.2.6`) the three `?website` branches return no
  op, so the request is 405 MethodNotAllowed (`src/rgw/rgw_rest_s3.cc:4644-4649@v19.2.6`, and
  the PUT and DELETE branches at `:4703` and `:4767`).
- **Encoding.** `src/rgw/rgw_website.h:194-217@v19.2.6`: RGWBucketWebsiteConf ENCODE_START(2,1)
  index_doc_suffix, error_doc, routing_rules, redirect_all, then v2's subdir_marker,
  listing_css_doc, listing_enabled; the sub-structs are each (1,1) (`:26-174`). radosgw
  decodes a v1 configuration and writes it back as v2 on any instance rewrite. Files identical
  at both tags. The bucket instance JSON (`radosgw-admin metadata get bucket.instance`) dumps
  `has_website` and, when set, `website_conf`, which is lossy: only `redirect_all` when its
  hostname is set, otherwise index_doc_suffix, error_doc and routing_rules
  (`src/rgw/rgw_website.cc:196-212@v19.2.6`; the dump and decode calls at
  `src/rgw/rgw_common.cc:2529-2531` and `src/rgw/rgw_common.cc:2566-2568@v19.2.6`, `:2591-2593` and
  `src/rgw/rgw_common.cc:2628-2630@v20.2.4`).
- **Which requests are website requests.** `rgw_enable_apis` must name `s3website`
  (`src/rgw/rgw_appmain.cc:301-312@v19.2.6`). A request is a website request when its Host is
  in the s3website hostnames, `rgw_dns_s3website_name` plus the zonegroup's
  `hostnames_s3website` (`src/rgw/rgw_rest.cc:235-238@v19.2.6`), or when `s3website` is listed
  before `s3` in `rgw_enable_apis`, which makes every request with a Host a website request
  (`src/rgw/rgw_rest.cc:2017-2179@v19.2.6#RGWREST::preprocess`). **Differs:** v20.2.4 also
  makes a request with no Host a website request in that case (`src/rgw/rgw_rest.cc:2045-2201@v20.2.4#RGWREST::preprocess`, its added
  else-branch). The website handler is chosen in
  `src/rgw/rgw_rest_s3.cc:5255-5277@v19.2.6#RGWRESTMgr_S3::get_handler` and formats errors as
  HTML.
- **Website serving.** Only GET and HEAD have an op, at every scope, and every other method is
  405 (`src/rgw/rgw_rest_s3website.h:20-72@v19.2.6`). After `init_permissions` and before
  `read_permissions` (`src/rgw/rgw_process.cc:180-190@v19.2.6`), `retarget`
  (`src/rgw/rgw_rest_s3.cc:5357-5418@v19.2.6#RGWHandler_REST_S3Website::retarget`): no bucket is
  NoSuchBucket; no website is NoSuchWebsiteConfiguration; no index suffix is 400 InvalidArgument
  "The IndexDocument Suffix is not configurated or not well formed!"; the effective key is the
  suffix for an empty key, key+suffix for a key ending in `/`, key+"/"+suffix when the key less
  one trailing `/` is not an existing object (`web_dir`, a stat, `:5316-5340`), else the key
  (`src/rgw/rgw_website.cc:116-133@v19.2.6#RGWBucketWebsiteConf::get_effective_key`); a
  RedirectAllRequestsTo, or the first routing rule whose KeyPrefixEquals prefixes the
  effective key and that names no error code, redirects with WebsiteRedirect, 301 or the rule's
  HttpRedirectCode, and a Location
  of protocol (the rule's, else http or https) `://` host (the rule's, else the Host header) `/`
  and the replaced key (`src/rgw/rgw_website.cc:40-114@v19.2.6`); otherwise the effective key
  is served as a GetObject. An object carrying `user.rgw.x-amz-website-redirect-location` is
  answered 301 with that Location and no body
  (`src/rgw/rgw_rest_s3.cc:266-284@v19.2.6#RGWGetObj_ObjStore_S3Website::send_response_data`).
  An error goes through the website error handler
  (`src/rgw/rgw_rest_s3.cc:5503-5555@v19.2.6#RGWHandler_REST_S3Website::error_handler`): a
  routing rule matching the original key and the error's HTTP status redirects; else the error
  document, when configured, is served by a nested GetObject with conditionals cleared and the
  original status (`:5430-5501#RGWHandler_REST_S3Website::serve_errordoc`); else, or when that
  fails, an HTML error page (`src/rgw/rgw_rest.cc:1803@v19.2.6`; ceph's HTMLFormatter writes
  `<html><head><title>STATUS NAME</title></head><body><h1>STATUS NAME</h1><ul>` and one
  `<li>Name: value</li>` per field). **Differs only in** `web_dir`'s stat call
  (`src/rgw/rgw_rest_s3.cc:5876-6145@v20.2.4`), not in behaviour.
- **Phase 1 interaction.** Website GETs are ordinary authenticated or anonymous GetObjects for
  ACL and policy. CompleteMultipartUpload's Location and the CNAME fallback know no s3website
  hostnames today (docs/exclusions.md entries named in §3).

### 2.3 Encryption configuration, "as an unconfigured radosgw"

- **The three ops do not depend on any key-management backend.** PutBucketEncryption (action
  s3:PutBucketEncryption, no tag loading) reads the body, refuses a parse failure or a missing
  `ServerSideEncryptionConfiguration` root with 400 MalformedXML, and stores whatever it
  decoded, any SSEAlgorithm string and any key id, in `user.rgw.sse-s3.policy`
  (`src/rgw/rgw_op.cc:8744-8799@v19.2.6#RGWPutBucketEncryption::execute`,
  `src/rgw/rgw_op.cc:9725-9780@v20.2.4#RGWPutBucketEncryption::execute`); 200, no op to `end_header`
  (`src/rgw/rgw_rest_s3.cc:3847-3854@v19.2.6`). GetBucketEncryption (s3:GetBucketEncryption):
  no attr is 404 ServerSideEncryptionConfigurationNotFoundError "The server side encryption
  configuration was not found"; an undecodable one 500
  (`src/rgw/rgw_op.cc:8809-8828@v19.2.6#RGWGetBucketEncryption::execute`); the body is
  `ServerSideEncryptionConfiguration` in the S3 namespace, Rule/ApplyServerSideEncryptionByDefault
  with SSEAlgorithm, KMSMasterKeyID when non-empty and BucketKeyEnabled only when true
  (`src/rgw/rgw_rest_s3.cc:3856-3874@v19.2.6`, `src/rgw/rgw_bucket_encryption.cc:8-40@v19.2.6`).
  DeleteBucketEncryption (authorized as s3:PutBucketEncryption) removes the attr and
  `user.rgw.sse-s3.key-id`, 204 also when absent
  (`src/rgw/rgw_op.cc:8838-8854@v19.2.6#RGWDeleteBucketEncryption::execute`). The only
  differences between the tags are the forward argument and Get's log level.
- **Encoding.** RGWBucketEncryptionConfig (1,1){rule_exist, rule when set},
  ServerSideEncryptionConfiguration (1,1){apply, bucketKeyEnabled},
  ApplyServerSideEncryptionByDefault (1,1){kmsMasterKeyID, sseAlgorithm}
  (`src/rgw/rgw_bucket_encryption.h:9-141@v19.2.6`, identical at v20.2.4). The ceph-object-corpus
  holds all three plus RGWCORSRule at 18.2.0 and 19.2.0-404-g78ddc7f9027, and ceph-dencoder knows
  RGWBucketEncryptionConfig and RGWCORSRule at both tags.
- **What the default does to a write.** PutObject, POST object and CreateMultipartUpload call
  `get_encryption_defaults` before authorizing
  (`src/rgw/rgw_rest_s3.cc:146-264@v19.2.6#get_encryption_defaults`, `src/rgw/rgw_rest_s3.cc:151-269@v20.2.4#get_encryption_defaults`; calls at
  `:2612`, `:2973`, `src/rgw/rgw_rest_s3.cc:3973@v19.2.6` and `:2773`, `:3118`, `src/rgw/rgw_rest_s3.cc:4454@v20.2.4`): SSE-C with SSE-KMS
  headers is 400 InvalidArgument; a stored configuration that does not decode is 400
  InvalidArgument "Server side error - can't decode bucket_encryption_conf"; SSE-C wins; the
  stored algorithm and key id fill the request's encryption headers when it names none, and so
  reach the policy environment. Then `rgw_s3_prepare_encrypt`
  (`src/rgw/rgw_crypt.cc:1030-1301@v19.2.6#rgw_s3_prepare_encrypt`): with
  `rgw_crypt_require_ssl` (default true) over plain HTTP it is 400 InvalidRequest; aws:kms
  without a key id is 400 InvalidArgument; an algorithm other than aws:kms and AES256 is 400
  InvalidArgument; with no key server, the defaults `rgw_crypt_s3_kms_backend=barbican` and
  `rgw_crypt_sse_s3_backend=vault` with empty addresses fail with EINVAL in
  `src/rgw/rgw_kms.cc:919-979@v19.2.6` and `:255-271`, so 400 InvalidArgument. That last
  status is by source reading; the Keystone token path was not traced to its end, so the task
  records it from a running radosgw before pinning it.
- **Phase 1 interaction.** Phase 1 answers 501 to any write into a bucket holding the attr
  (`internal/op/putobject.go:21-33` @0239625) and leaves the default out of the policy
  environment (docs/exclusions.md, "A PUT's encryption condition keys leave out the bucket's
  default encryption").

### 2.4 Ownership controls

- **Neither floor has the feature.** The only traces are the IAM action names
  (`src/rgw/rgw_iam_policy.cc:84`, `src/rgw/rgw_iam_policy.cc:117@v19.2.6`; `:84`, `src/rgw/rgw_iam_policy.cc:120@v20.2.4`). `ownershipControls`
  is no sub-resource (`src/rgw/rgw_common.cc:918-976@v19.2.6#RGWHTTPArgs::append`) and no op
  branch tests it, so a floor radosgw answers GetBucketOwnershipControls with a ListObjects,
  PutBucketOwnershipControls as a CreateBucket and DeleteBucketOwnershipControls as a
  **DeleteBucket**, which removes an empty bucket. CreateBucket ignores
  `x-amz-object-ownership`. rgw-go's dispatch falls through the same way today.
- **Upstream implements it from v21.0.0** (b3733296c8e, 2025-06-24). Its stored form is a
  bucket attr `user.rgw.ownership-controls` holding ENCODE_START(1,1) and a u8 enum
  BucketOwnerEnforced=0, BucketOwnerPreferred=1, ObjectWriter=2; a missing or undecodable attr
  means ObjectWriter (`src/rgw/rgw_object_ownership.cc:77-106@v21.1.2`); the ops are
  `src/rgw/rgw_op.cc:10247-10358@v21.1.2`; PutBucketOwnershipControls refuses
  BucketOwnerEnforced on a bucket whose ACL is not the default private one with 400
  InvalidBucketAclWithObjectOwnership, Get without one is 404 OwnershipControlsNotFoundError,
  and PutACLs refuses ACLs under BucketOwnerEnforced (`src/rgw/rgw_op.cc:6868-6869@v21.1.2`). s3-tests at the
  PIN has 8 `object_ownership` tests (`test_s3.py:21557-21662`), excluded by run.sh's markers.

### 2.5 Public access block

- **PutPublicAccessBlock** (s3:PutBucketPublicAccessBlock, tags as above): parse failure or no
  `PublicAccessBlockConfiguration` root is 400 MalformedXML; each of the four booleans is
  optional; the block is stored in `user.rgw.public-access`, 200, no op to `end_header`
  (`src/rgw/rgw_op.cc:8600-8662@v19.2.6`, `src/rgw/rgw_op.cc:9581-9643@v20.2.4`;
  `src/rgw/rgw_public_access.cc:4-9@v19.2.6`). **Its retry merges the attrs the request
  started with** (`src/rgw/rgw_op.cc:8656-8660@v19.2.6`), as PutBucketCors's does
  (`:6100-6104`), so a retry writes back another attr a concurrent write had just changed: the
  registry's PutBucketPolicy defect, in two more ops.
- **GetPublicAccessBlock** (s3:GetBucketPublicAccessBlock): no attr is 404
  NoSuchPublicAccessBlockConfiguration "The public access block configuration was not found";
  an undecodable one 500 (`src/rgw/rgw_op.cc:8664-8699@v19.2.6`). The body is
  `PublicAccessBlockConfiguration` **without** the S3 namespace, the four booleans as
  `true`/`false` (`src/rgw/rgw_public_access.cc:11-20@v19.2.6`). Its `send_response` writes
  that document whatever `op_ret` is
  (`src/rgw/rgw_rest_s3.cc:4583-4594@v19.2.6#RGWGetBucketPublicAccessBlock_ObjStore_S3::send_response`),
  so the 404 carries the error document and its Content-Length (`src/rgw/rgw_rest.cc:620-625@v19.2.6`)
  and then a default block past that length; no IO filter truncates it
  (`src/rgw/rgw_client_io_filters.h:327-365@v19.2.6`).
- **DeletePublicAccessBlock** (authorized as s3:PutBucketPublicAccessBlock): removes the attr,
  204 also when absent (`src/rgw/rgw_op.cc:8702-8742@v19.2.6`).
- **Enforcement is phase 1's and unchanged.** rgw-go already honours a stored block on both
  releases, with Tentacle's RestrictPublicBuckets (§3). The PAB ops add only configuration.
  Upstream main changed nothing in the per-bucket form (`src/rgw/rgw_public_access.h:25-45@292995374d9`).

### 2.6 Policy status

- GetBucketPolicyStatus (s3:GetBucketPolicyStatus, tags as above) answers IsPublic =
  `is_public(bucket policy) || bucket ACL is_public`, ignoring the block
  (`src/rgw/rgw_op.cc:8582-8598@v19.2.6`, `src/rgw/rgw_op.cc:9563-9579@v20.2.4`), in `PolicyStatus` in the S3
  namespace with lower-case `true`/`false`
  (`src/rgw/rgw_rest_s3.cc:4554-4572@v19.2.6#RGWGetBucketPolicyStatus_ObjStore_S3::send_response`).
  **Differs:** `is_public` itself differs between the tags (the registry's "Squid's is_public
  counts every Allow statement whose NotPrincipal is not the wildcard"), and both share
  "radosgw's is_public judges a wildcard-principal statement against a fixed three-key
  environment". rgw-go's `policy.Policy.IsPublic(policy.SemanticsFor(release))` and
  `acl.Policy.IsPublic` already exist.

### 2.7 POST object

- **Where it authenticates.** POST object has no Authorization header, so `verify_requester`
  makes it anonymous and the suspended-user check passes it
  (`src/rgw/rgw_process.cc:372-376@v19.2.6`, `src/rgw/rgw_process.cc:374-378@v20.2.4`). After `init_permissions`
  (the bucket must exist), `init_processing` reads the form and authenticates the form's
  credentials, replacing the identity, its user and its identity policies
  (`src/rgw/rgw_op.cc:4576-4590@v19.2.6#RGWPostObj::init_processing`;
  `src/rgw/rgw_rest_s3.cc:3182@v19.2.6`, `src/rgw/rgw_rest_s3.cc:3326@v20.2.4`; `src/rgw/rgw_auth.cc:489-555@v19.2.6`).
  Then the op mask, then s3:PutObject against the key's ARN through
  `verify_bucket_permission`, with the encryption headers in the environment
  (`src/rgw/rgw_op.cc:4592-4605@v19.2.6#RGWPostObj::verify_permission`).
- **The form.** radosgw's own multipart reader, identical at both tags
  (`src/rgw/rgw_rest.cc:1107-1471@v19.2.6`, `src/rgw/rgw_rest.cc:1112-1476@v20.2.4`): Content-Type must be
  exactly `multipart/form-data` with a `boundary` (400 InvalidArgument otherwise); a 100
  Continue is sent first; part headers are lines trimmed of white space, read up to
  `rgw_max_chunk_size`, and the Content-Disposition `name` names the part; part names compare
  case-insensitively; values are trimmed when read. No Content-Length is 411
  MissingContentLength and one over `rgw_max_put_size` is 400 EntityTooLarge
  (`:1408-1424#RGWPostObj_ObjStore::verify_params`). Parts are read up to the `file` part,
  whose Content-Disposition `filename` is kept; reaching the end first is 400 "Malformed
  request"; parts after the file are read and discarded.
- **Parameters** (`src/rgw/rgw_rest_s3.cc:2906-3070@v19.2.6#RGWPostObj_ObjStore_S3::get_params`):
  every part before the file enters the policy environment with its raw value; the
  `x-amz-server-side-encryption*` parts feed `get_encryption_defaults`; **differs:** v20.2.4
  also maps `x-amz-checksum-*` parts into the request environment for the checksum filter
  (`src/rgw/rgw_rest_s3.cc:3040-3214@v20.2.4#RGWPostObj_ObjStore_S3::get_params`); `bucket` is set; `key` is required (400 "Key not specified"), its
  first `${filename}` is replaced (`:2881-2894#RGWPostObj_ObjStore_S3::rebuild_key`), and an
  empty result is 400 "Empty object name"; `Content-Type`; `x-amz-storage-class` must be a
  valid placement; `x-amz-meta-*` and `x-amz-website-redirect-location` parts become attrs;
  `tagging` is a Tagging document (`:3072-3110`).
- **Credentials and the policy document**
  (`src/rgw/rgw_rest_s3.cc:3112-3246@v19.2.6#RGWPostObj_ObjStore_S3::get_policy`, identical at
  v20.2.4). With a `policy` part: `x-amz-algorithm` equal to AWS4-HMAC-SHA256 selects SigV4,
  which needs `x-amz-credential`, `x-amz-signature` and `x-amz-date`; otherwise SigV2 needs
  `AWSAccessKeyId` and `signature`; each missing part is 400; an empty `x-amz-security-token`
  is 400 "Invalid token"; a failed authentication is 403. SigV2 signs the base64 policy with
  HMAC-SHA1; SigV4 takes the key id from the credential up to its first `/`, signs the base64
  policy with the signing key of the rest as scope, and checks neither the date nor the region
  (`src/rgw/rgw_rest_s3.cc:6117-6171@v19.2.6`). The document
  (`src/rgw/rgw_policy_s3.cc:238-305@v19.2.6#RGWPolicy::from_json`): malformed JSON, no
  `expiration`, an expiration that `parse_iso8601` refuses, no `conditions`, an array
  condition without exactly three elements, an operator other than eq, starts-with or
  content-length-range, or a bad length are 400; an object condition checks its first member
  only. The check (`:189-235#RGWPolicy::check`): expired (expires <= now), a simple check
  whose field is missing or differs, a failed condition, or a form field that no condition
  checked (`x-ignore-*` excepted) are 403 with radosgw's message; **differs:** v20.2.4 also
  excepts checksum fields (`src/rgw/rgw_policy_s3.cc:108-130@v20.2.4`). Without a `policy`
  part the request stays anonymous. The `acl` part is a canned ACL ("Bad canned ACLs" on
  failure). No public-access-block check is made on it.
- **Execute** (`src/rgw/rgw_op.cc:4612-4796@v19.2.6#RGWPostObj::execute`,
  `src/rgw/rgw_op.cc:4905-5139@v20.2.4#RGWPostObj::execute`): the quota check; Content-MD5 (400 InvalidDigest or BadDigest); a random
  instance on a versioned bucket; radosgw's atomic writer, the same RADOS writes as a
  PutObject (index prepare and complete with the reshard guard, head and tails, GC of an
  overwritten object, default retention applied in the head write as for PutObject,
  `src/rgw/driver/rados/rgw_rados.cc:3179-3183@v19.2.6`); encryption or compression filters; a
  body over the policy's maximum is 400 EntityTooLarge and under its minimum 400 EntityTooSmall;
  attrs ETag, ACL, Content-Type (NUL-terminated), compression, metas, tags.
  **Differs:** v20.2.4 adds the streaming checksum filter and `user.rgw.cksum`.
- **Response** (`src/rgw/rgw_rest_s3.cc:3297-3412@v19.2.6#RGWPostObj_ObjStore_S3::send_response`):
  `success_action_redirect` is 303 with Location `<redirect>?bucket=…&key=…&etag=…` (tenant
  first when set); `success_action_status` 200 is 200, 201 is 201 with a `PostResponse` of
  Location (`compute_domain_uri`), Bucket, Key and ETag, anything else 204; the default is 204
  with the ETag header.

### 2.8 Errors used

From `src/rgw/rgw_common.cc@v19.2.6` (one line lower at v20.2.4): 301 WebsiteRedirect (:58), 400
InvalidArgument (:61), InvalidRequest (:62, and ERR_INVALID_CORS_RULES_ERROR and
ERR_INVALID_WEBSITE_ROUTING_RULES_ERROR at :82-83), MalformedXML (:77), EntityTooSmall (:75),
411 MissingContentLength (:87), 404 NoSuchWebsiteConfiguration (:99), NoSuchCORSConfiguration
(:106, :109), ServerSideEncryptionConfigurationNotFoundError (:138),
NoSuchPublicAccessBlockConfiguration (:139); 201, 204 and 303 for POST (:53, :55, :59). rgw-go
already defines every one (`internal/op/errors.go:62-119` @0239625).

## 3. What rgw-go already has

All paths and lines at origin/main 0239625.

**Dispatch and handler.**
- Every route of the area except ownership controls is already dispatched with radosgw's
  precedence: bucket-scope GET `website`, `cors`, `policyStatus`, `publicAccessBlock`,
  `encryption`; PUT `website`, `cors`, `publicAccessBlock`, `encryption`; DELETE `cors`,
  `publicAccessBlock`, `encryption`, `website`; POST's default `post_obj` at bucket and object
  scope; OPTIONS `options_cors` at bucket and object scope (`internal/s3/dispatch.go:120-250`).
  The table's comment already says the `?website` rows' handlers apply
  `rgw_enable_static_website` (`:111-119`).
- `payloadTable` lists the signed-payload forms of put_cors, the three encryption routes,
  set_bucket_website, delete_bucket_website and the three public-access-block routes
  (`internal/s3/dispatch.go:309-345`).
- A dispatched route with no bound handler answers 501 after authentication, `PostAuthInit`
  and the suspended check (`internal/s3/handler.go:377-381`); `bucketHandlers` binds none of
  this area's routes (`internal/s3/bucket.go:22-41`). Must change: each feature binds its routes
  (hot-file plan in §7).
- `SetCommonHeaders` is the place `end_header`'s headers are written, used by `writeEmpty`,
  `writeXML`, the error document and 12 other call sites (`internal/s3/headers.go:16-26`):
  the CORS response headers hook in here. `WriteError` and `writeErrorDocument` render the XML
  error document (`internal/s3/errors.go:37-53`): website mode needs an HTML variant.
- `countingReader` counts request bytes for put_obj only (`internal/s3/handler.go:356-358`,
  `:473-486`): POST object needs it too (main's usage fix, §6).
- Helpers to reuse: `readParamBody` (`internal/s3/bucket.go:135`), `writeDocument`
  (`internal/s3/objectdocs.go:40`), `writeXML` (`internal/s3/xml.go:33`), `writeEmptyXML`
  (`internal/s3/bucketsubres.go:144-147`), `completeLocation`'s domain logic
  (`internal/s3/multipart.go:341-360`), `op.URLEncode`, `xmltext`.

**Op layer.**
- The bucket-subresource pattern to copy: Init `loadBucket`, `verifyBucketAction`,
  `retryBucketWrite` setting only the op's own attr and re-authorizing each retry, the error
  sentinels (`internal/op/bucketsubres.go:56-90`, `:253-406`). Every error the area needs is
  defined (`internal/op/errors.go:62-119`).
- `op.AttrPublicAccess` (`internal/op/putobject.go:35-37`) and `op.AttrBucketEncryption`
  (`:17-19`) exist. `bucketEncryptionUnserved` answers 501 for any write into a bucket holding
  the encryption attr (`:21-33`), called from PutObject (`:150`), CopyObject
  (`internal/op/copyobject.go:206`), UploadPart (`internal/op/uploadpart.go:214`) and
  CreateMultipartUpload (`internal/op/initmultipart.go:84`); `s3.refuseEncryption` answers 501
  to every write that asks for encryption (`internal/s3/putobject.go:144-171`). Must change:
  SSE-S3 and SSE-KMS get the unconfigured radosgw's answer, a default configuration without an
  algorithm stops refusing, SSE-C keeps 501 until phase 4.
- `BucketStore.PutBucketInfo` rewrites the instance, `PutBucketAttrs` its attrs, both guarded
  (`internal/op/bucket.go:114-117`).
- `op.Run` runs Init, VerifyPermission, the op mask, then Execute (`internal/op/op.go:60-79`):
  main's CORS match goes between the op mask and the permission verdict.

**Authorization (phase 1's seams, which PAB and POST object plug into).**
- A stored public-access block is honoured on both releases already:
  `authz.publicAccess` decodes it, refusing an undecodable one
  (`internal/authz/load.go:70-92`); `requestBucket`, `explicitBucket` and `explicitPrelude`
  carry it with the bucket inputs (`internal/authz/evaluator.go:249-345`); `restricted` applies
  RestrictPublicBuckets with Tentacle's `is_public` (`:581-598`); `ignorePublicACLs` (`:600-603`);
  the write refusals for BlockPublicAcls and BlockPublicPolicy are in
  `internal/authz/write.go:112-159` and `:255-267` and in
  `op.blockPublicACLs`/`op.blocksPublicPolicy` (`internal/op/putobject.go:198-211`,
  `internal/op/bucketsubres.go:352-363`). **So the PAB ops plug in only by writing and removing
  `user.rgw.public-access` through `PutBucketAttrs`**; the cache-notified bucket record then
  carries the block to every check, with no change to authz.
- POST object plugs in at two points. Its form credentials resolve to an `op.Identity`
  through `internal/auth` (a new entry beside `Verifier.Authenticate`,
  `internal/auth/verifier.go:51-108`, reusing `lookup` at `:147-181` and `identity` at
  `internal/auth/identity.go:30`), and the
  op then authorizes `s3:PutObject` on the key through the existing
  `op.VerifyBucketPermission`, the PutObject check, with the form's encryption fields in the
  environment; its canned ACL is refused under BlockPublicAcls through `op.blockPublicACLs`.
- The bucket-tag rules already list the CORS, website, PAB and policy-status actions, and say
  the encryption ops add none (`internal/authz/tagrule.go:120-143`). The policy actions exist,
  ownership's included (`internal/policy/action.go:39-40`, `:86-87`).

**Authentication.** An unsigned OPTIONS is anonymous and a POST without Authorization is
anonymous (`internal/auth/verifier.go:62`, `internal/auth/request.go:179-196`); a signed OPTIONS
is verified over the method OPTIONS (`internal/auth/sigv4.go:404-406`,
`internal/auth/sigv2.go:98-100`).

**Types.**
- `acl.PublicAccessBlock` with Encode/Decode at v1 (`internal/acl/publicaccess.go:9-38`); no XML
  yet. `acl.Policy.IsPublic` and `policy.Policy.IsPublic(policy.SemanticsFor(release))` exist.
- `meta.BucketInfo.Website` is an opaque `RawStruct`, decoded with version check 2 and written
  back unchanged (`internal/meta/bucket_info.go:69-75`, `:101-102`, `:173-176`, `:262-264`); its
  JSON dump and decode refuse a bucket with a website (`:351-356`;
  `internal/meta/bucket_json.go:98-126`; `internal/meta/dump_bucket.go:46-51`), so the admin
  metadata API answers 501 for it (`internal/driver/metadata.go:90`). Must change: model it.
- `meta.AttrWebsiteRedirect` exists and GetObject already sends
  `x-amz-website-redirect-location` (`internal/meta/attrs_read.go:27`,
  `internal/s3/getobject.go:54`).
- `cephconf.EnabledAPIs` treats `s3website` as S3 and logs it as ignored
  (`internal/cephconf/apis.go:18-41`); `internal/vhost` knows no s3website hostnames.
- No CORS, website-XML, encryption-config, ownership, POST-form or POST-policy type exists.
- No fakerados work: nothing in this area calls an object class beyond what PutObject and the
  bucket-instance writes already emulate.

**Exclusions in force that phase 2 retires or rewrites** (docs/exclusions.md, by title):
- "A signed OPTIONS request is verified over OPTIONS and answered 501 until CORS lands."
- "Writes that ask for encryption answer 501, SSE-S3 and SSE-KMS until phase 2 and SSE-C until
  phase 4." (the SSE-S3/KMS half), and "A PUT's encryption condition keys leave out the
  bucket's default encryption."
- "PutObject and CopyObject authorize a request before they read what the store holds for it",
  whose last sentence says rgw-go serves no POST-object.
- "Some bucket sub-records are carried opaque in phase 0." (the website half) and the metadata
  API's 501 for a document naming a website configuration.
- "The CNAME fallback resolves no CNAME and knows no s3website hostnames." and
  "CompleteMultipartUpload's Location finds its domain without CNAME lookups or website
  hostnames." (the website-hostname halves).
- "Key management: SSE-KMS and SSE-S3 with every backend", item 2, rewritten for the
  validation §6 proposes.
- Extended rather than retired: "A block of public ACLs refuses a public ACL on PutObject,
  CopyObject, CreateMultipartUpload and UploadPart" (adds POST object), "A retried
  PutBucketPolicy keeps what the write it lost to changed" (adds PutBucketCors and
  PutPublicAccessBlock), "A PutBucketAcl that loses a race is retried, and every retried ACL,
  policy or tagging write is authorized again" (adds the new bucket writes).

## 4. Coexistence

**What both gateways must read and write identically.**

| State | Where | Form both write | Notes |
|---|---|---|---|
| CORS configuration | bucket instance xattr `user.rgw.cors` | RGWCORSConfiguration (1,1) of RGWCORSRule (1,1) | sets are sorted on encode, as `std::set` encodes; a rule's 0 methods and an invalid max age (0xffffffff) round-trip |
| Website configuration | RGWBucketInfo `has_website` + `website_conf` | RGWBucketWebsiteConf (2,1) | a stored v1 is decoded and re-encoded as v2 on any instance rewrite, as radosgw does (retires phase 0's write-back of the v1 bytes) |
| Encryption configuration | xattr `user.rgw.sse-s3.policy` | RGWBucketEncryptionConfig (1,1) | Delete also removes `user.rgw.sse-s3.key-id`, which only a radosgw with an SSE-S3 backend writes |
| Public access block | xattr `user.rgw.public-access` | PublicAccessBlockConfiguration (1,1), four bools | phase 1 already reads it |
| Ownership controls (if implemented) | xattr `user.rgw.ownership-controls` | v21's (1,1){u8} | a floor radosgw ignores it and carries it through its attr merges; an Umbrella radosgw enforces it |
| POST-object objects | head, tails, index, GC, usage log | as PutObject writes them: ETag, ACL, NUL-terminated content type, `x-amz-meta-*`, website redirect, tags, compression, Tentacle's `user.rgw.cksum` | the usage entry counts the body bytes (main's fix, §6) |
| Bucket instance JSON | admin metadata get/put of `bucket.instance` | radosgw's lossy `website_conf` dump and decode | a put through JSON drops `subdir_marker`, `listing_css_doc` and `listing_enabled`, as radosgw's does |

Every attr write is a guarded instance rewrite with radosgw's cache notify, so a write through
one gateway is seen by the other within the notify; no new cache key, lock or class call.

**Behaviour that differs by design (both serve the same RADOS state; each is an exclusion):**
CORS rule selection and the non-preflight refusal follow main; website-endpoint and CORS
headers follow §2 with the fixes in §6; POST object refuses a suspended user and a public
canned ACL under a block; ownership controls, if implemented, are enforced by rgw-go and
ignored by a floor radosgw.

**Cross-gateway gate cases** (cluster; write with one gateway, read with the other):
1. CORS: radosgw's PutBucketCors read by rgw-go's GetBucketCors (document equal) and honoured
   by its preflight and response headers; rgw-go's PutBucketCors read by radosgw's; the
   `user.rgw.cors` bytes equal for the same document, rules with wildcards, several origins,
   expose headers and max age included.
2. Website: radosgw's PutBucketWebsite (index, error document, routing rules, redirect-all)
   served by rgw-go's website endpoint; rgw-go's served by radosgw's; the bucket instance equal
   through ceph-dencoder's RGWBucketInfo dump; a v1 configuration rewritten by each comes out
   v2; `radosgw-admin metadata get bucket.instance` equal to rgw-go's admin get.
3. Public access block: each gateway's PutPublicAccessBlock read back by the other and
   enforced by the other (BlockPublicAcls on PutBucketAcl, IgnorePublicAcls on an anonymous
   GET, BlockPublicPolicy on PutBucketPolicy, RestrictPublicBuckets on Tentacle); attr bytes
   equal.
4. Encryption configuration: each writes AES256 and aws:kms configurations the other reads; attr
   bytes equal (also against the corpus); a PutObject into such a bucket refused by both with
   the same status over TLS and over plain HTTP, recorded from radosgw.
5. Policy status: the same IsPublic from both for public-read ACLs, a public policy and a
   private bucket.
6. POST object: an rgw-go POST read by radosgw (GET body, ETag, Content-Type, metadata, ACL,
   tags) and by `radosgw-admin object stat` and `bucket check`; a radosgw POST read by rgw-go;
   the usage log's bytes for each.
7. Ownership controls (if implemented): rgw-go's attr survives a radosgw PutBucketAcl and
   PutBucketTagging (merge keeps unknown attrs) and is read back by rgw-go.

## 5. Tests

### 5.1 s3-tests

At the PIN f6f2529dd9ef, test_s3.py. radosgw's outcome for **none** of these is recorded: the
ledgers (`test/s3tests/baseline/{squid,tentacle}.json` @0239625) hold 416 tests each, all
outside this area. Each feature's recording task deletes its deselect lines and records both
ledgers (`make s3tests-record-tests`) before rgw-go's outcomes are compared.

| Feature | Tests | How they are held out today |
|---|---|---|
| CORS | 17: test_set_cors, test_cors_origin_response, test_cors_origin_wildcard, test_cors_header_option, test_object_raw_get_x_amz_expires_not_expired{,_tenant}, and the 11 test_cors_presigned_* (get_object, get_object_tenant, get_object_v2, get_object_tenant_v2, put_object, put_object_with_acl, put_object_v2, put_object_tenant_v2, put_object_tenant, put_object_tenant_with_acl, url_non_preflight) | `hack/s3tests/deselect-phase2.txt` lines 92-108 |
| Public access block | 10: test_get_undefined_public_block, test_get_public_block_deny_bucket_policy, test_put_public_block, test_block_public_put_bucket_acls, test_block_public_object_canned_acls, test_block_public_policy, test_block_public_policy_with_principal, test_block_public_restrict_public_buckets, test_ignore_public_acls, test_put_get_delete_public_block | lines 111-120 |
| Policy status | 6: test_get_bucket_policy_status, test_get_public_acl_bucket_policy_status, test_get_authpublic_acl_bucket_policy_status, test_get_publicpolicy_acl_bucket_policy_status, test_get_nonpublicpolicy_acl_bucket_policy_status, test_get_nonpublicpolicy_principal_bucket_policy_status | lines 123-128 |
| POST object | 35, lines 55-89 (all SigV2; none uses `x-amz-algorithm`, so SigV4 POST has no s3-test) | lines 55-89 |
| Encryption configuration | 6: test_put/get/delete_bucket_encryption_s3 (`sse_s3`) and _kms (`encryption`), `test_s3.py:15343-15435` | run.sh's EXCLUDED_MARKERS, with the SSE write tests the same markers carry (`hack/s3tests/run.sh:66-72`) |
| Ownership controls | 8 `object_ownership` tests, `test_s3.py:21557-21662` | EXCLUDED_MARKERS |
| Website | none: pytest.ini declares `s3website`, `s3website_routing_rules` and `s3website_redirect_location` but no test at the PIN carries them | - |

Outcomes the source predicts and the recording must confirm (none is reproduced):
- radosgw fails the four `_v2` presigned CORS tests, marked `fails_on_rgw`.
- radosgw is expected to fail test_cors_origin_response (it answers 200 where the test wants
  403 for a plain GET with a non-matching Origin, `test_s3.py:7187`) and
  test_cors_presigned_url_non_preflight (403 wanted for a method no rule allows): both need
  main's verify_cors_match, which no tag carries. rgw-go, following main, should pass them:
  each becomes a `known-differences-{squid,tentacle}.txt` line under its exclusion.
- If ownership controls are implemented, all 8 fail on radosgw at both floors and pass on
  rgw-go: known differences again, and the marker leaves EXCLUDED_MARKERS.
- The 6 encryption-configuration tests should pass on both (the ops need no backend); running
  them needs an include list for marker-excluded tests, which run.sh lacks (open question 7).
- test_post_object_upload_checksum (`checksum` marker) and the SSE POST tests belong to the
  writepath area and to the excluded key-management groups.

### 5.2 go-ceph rgw/admin suite

None. `hack/admin/deselect-phase2.txt` lists only versioning and object-lock subtests.

### 5.3 Specs the plan must require

- **fakerados:** no emulator work. Every write in the area is a bucket-instance rewrite or a
  PutObject the emulator already serves.
- **Corpus goldens** (hack/goldens): RGWCORSRule, RGWBucketEncryptionConfig,
  ServerSideEncryptionConfiguration and ApplyServerSideEncryptionByDefault, which the
  ceph-object-corpus holds at 18.2.0 and 19.2.0-404-g78ddc7f9027; each decodes to
  ceph-dencoder's JSON and re-encodes byte-identically.
- **Generated vectors** for the types ceph-dencoder lacks at both tags (RGWCORSConfiguration,
  RGWBucketWebsiteConf, PublicAccessBlockConfiguration, OwnershipControls): bytes captured
  from radosgw-written attrs and instances on a disposable cluster, with RGWBucketInfo, which
  ceph-dencoder knows, carrying the website configuration at v1 (hand-built) and v2.
- **Unit specs, per task** (§7 names them): XML parse and render tables from `xml_end`,
  `decode_xml` and `get_params`; the CORS matching table of test_cors_origin_response
  reproduced in Go plus the rule-selection case where the floors and main differ; the
  preflight's error order; website `get_effective_key`, `should_redirect` and `apply_rule`
  tables; the POST form reader against radosgw's framing cases (CRLF, a boundary split across
  reads, a final `--`, parts after the file, a missing boundary); the POST policy document's
  every error message; the browser-upload SigV2 and SigV4 vectors (AWS's published POST policy
  example for SigV4); the op error order with counterfeiter fakes; the handlers'
  status, headers and bodies.
- **Integration specs** (`//go:build integration`, cluster): the gate cases of §4, and the
  recording of radosgw's answers the plan pins only from a running release: the unconfigured
  SSE-S3 and SSE-KMS write errors, GetPublicAccessBlock's 404 framing, and the POST
  suspended-user and public-ACL cases (§6).

## 6. Floor defects

Classes are the owner's: security, data corruption, S3-correctness, cosmetic. "Unreproduced"
means derived from the source; each new entry goes into `docs/ceph-upstream-bugs.md` with that
status, and the recording task of its feature budgets the cluster reproduction (PP-11). None of
the new ones has had a prior-art search yet; the task that records an entry runs it.

| # | Defect (floors: v19.2.6 and v20.2.4 unless stated) | Class | Status | Upstream | rgw-go's choice |
|---|---|---|---|---|---|
| 1 | A non-preflight request with an Origin no CORS rule allows is served (`generate_cors_headers` only omits headers) | S3-correctness | unreproduced; s3-tests test_cors_origin_response and test_cors_presigned_url_non_preflight expect the refusal | fixed on main by 4e056f98e78, 8d652cd24c1, 7f147f02058 (`src/rgw/rgw_op.cc:2088-2133@292995374d9#RGWOp::verify_cors_match`, called at `src/rgw/rgw_process.cc:295-299@292995374d9`); in no tag | follow main: 403 AccessDenied after the op mask, before the permission verdict, on a bucket with a CORS configuration; new exclusion |
| 2 | The rule chosen is the first whose origin matches, and its method and headers are then tested, so a later rule that allows the request is never used (`src/rgw/rgw_cors.cc:186-194@v19.2.6#RGWCORSConfiguration::host_name_rule`) | S3-correctness | unreproduced | fixed on main by cc1dcecee08 (PR #69190), "match CORS rules like Amazon S3" | follow main's first-rule-matching-origin-method-and-headers, for preflight, response headers and #1; AWS documents the same (`userguide/cors.html`); new exclusion |
| 3 | A signed preflight's signature is never compared (registry, "radosgw grants a signed CORS preflight without comparing its signature") | quirk (AWS checks none either); its rate-limit charge is security | unreproduced | reported to security@ceph.io 2026-10-05 | take the skip; record that phase 3's rate-limit enforcement must not charge OPTIONS CORS to the key's user (open question 5) |
| 4 | PutBucketCors and PutPublicAccessBlock retries merge the attrs the request started with, writing back a concurrent change to another attr (`src/rgw/rgw_op.cc:6100-6104`, `src/rgw/rgw_op.cc:8656-8660@v19.2.6`) | data corruption (lost update of shared bucket state) | unreproduced | none found; same defect as the registry's PutBucketPolicy entry | set only the op's own attr on each try (rgw-go's existing pattern); extend that entry and its exclusion |
| 5 | Retried bucket writes are not authorized again (registry entry) | security | unreproduced | registry | re-authorize each retry, as phase 1 does, for the new writes |
| 6 | GetPublicAccessBlock on a bucket without a block sends a default block document after the 404 error document, past its Content-Length (`src/rgw/rgw_rest_s3.cc:4583-4594@v19.2.6`) | S3-correctness (HTTP framing; can desynchronize a keep-alive connection) | unreproduced; the recording task captures the raw bytes | none found | send the error document alone; new exclusion |
| 7 | A public-access block that does not decode is ignored (registry entry) | security, low | unreproduced | registry | phase 1 already fails closed; the Get op answers such an attr 500 as radosgw's EIO |
| 8 | POST object is never checked for a suspended user: the check precedes the form's authentication (`src/rgw/rgw_process.cc:372-376@v19.2.6`, `src/rgw/rgw_process.cc:374-378@v20.2.4`) | security (fail-open: a suspended user uploads) | unreproduced | none found; prior-art search pending | refuse with 403 UserSuspended once the form's credentials resolve; new exclusion and registry entry, reported upstream once reproduced |
| 9 | POST object stores a public canned `acl` under BlockPublicAcls (no check in `RGWPostObj` or `get_policy`) | security (an owner's block bypassed) | unreproduced | none found; extends the registry's "radosgw lets a public ACL through a block of public ACLs on PutObject's grant headers, CopyObject and CreateMultipartUpload" | refuse with 403 AccessDenied once authorized, as rgw-go's PutObject does; extend that entry |
| 10 | POST object's body bytes are not counted in the usage log | S3-correctness (usage accounting, which is shared RADOS state) | reported upstream (tracker #76157) | fixed on main by 7d2484c45b4; in no tag | count them, as main does; new exclusion |
| 11 | Ownership-controls requests fall through to ListObjects, CreateBucket and DeleteBucket; DELETE `?ownershipControls` deletes an empty bucket | S3-correctness, losing the bucket | unreproduced | the feature lands in v21.0.0 (b3733296c8e) | open question 1; at the least the three requests must never run another op |
| 12 | PutBucketEncryption stores any SSEAlgorithm and a KMS key id with any algorithm (no validation in `src/rgw/rgw_bucket_encryption.cc:8-35@v19.2.6`) | S3-correctness | unreproduced | none found | validate as AWS documents (`API_ServerSideEncryptionByDefault.html`), 400 MalformedXML otherwise; the stored form is unchanged (open question 6) |
| 13 | Squid's `is_public` counts Allow statements without a wildcard principal (registry) | S3-correctness (over-block) | unreproduced | fixed in v20.0.0 | PolicyStatus uses the same `Policy.IsPublic` as BlockPublicPolicy; the semantics are open question 4 |
| 14 | `is_public` judges conditions against a fixed three-key environment (registry) | security | unreproduced | reported to security@ceph.io | as #13: one decision for every consumer |
| 15 | PutBucketWebsite answers an unparseable body 400 InvalidArgument, where AWS answers MalformedXML | S3-correctness, minor | unreproduced | none | per open question 3 |
| 16 | PAB's document lacks the S3 namespace; PolicyStatus and PAB booleans are lower-case | cosmetic (SDKs accept both) | source | none | per open question 3 (recommended: radosgw's) |
| 17 | POST's 201 Location under a configured domain has no scheme (registry's CompleteMultipartUpload entry, same `compute_domain_uri`) | cosmetic | unreproduced | registry | as rgw-go's CompleteMultipartUpload decided |

Not defects, recorded so no task re-derives them: default retention is applied to POST objects
by the head write, as for PutObject (`src/rgw/driver/rados/rgw_rados.cc:3179-3183@v19.2.6`);
`should_redirect`'s write of 301 into the configuration after copying the rule leaves every
redirect-all at 301 anyway; SSE-S3 and SSE-KMS fail on an unconfigured radosgw by design
(the key-management exclusion).

## 7. Proposed tasks

Thirty-nine tasks with ownership controls implemented (open question 1, option A): BC-1 to
BC-27, BC-28a to BC-28f and BC-29 to BC-34. Thirty-four with option B, whose single dispatch
task replaces BC-28a to BC-28f. Each task's brief cites the §2 lines it reproduces and writes its own exclusions and
registry entries (the standing rule). "Cluster" marks the tasks that need one; every other task
runs on unit specs and counterfeiter fakes.

### 7.1 Hot files and how to avoid serializing on them

| File | Tasks here | Other areas | Plan |
|---|---|---|---|
| `internal/s3/bucket.go` (`bucketHandlers`) and `handler.go` (`NewHandler`) | every handler task | versioning, lock-lifecycle | BC-1 makes route binding a list of per-feature route sets, one file each, wired once; later tasks touch only their own file. Offer the mechanism to every phase 2 area. |
| `internal/s3/handler.go` (serve, refusals) | BC-15, BC-33 | - | BC-15 owns the website-mode error path; BC-33 changes only the counting condition; merge in that order |
| `internal/s3/headers.go` | BC-7 | - | single owner |
| `internal/s3/dispatch.go` | BC-15 (website dispatch), BC-28b (ownership rows) | possibly versioning | website dispatch is a separate function in BC-15's own file, called from one line at the top of `Dispatch`; ownership adds rows only |
| `internal/op/op.go` (`Run`) | BC-4 | - | single owner; one call into `internal/op/corsmatch.go` |
| `internal/op/request.go` | BC-13 (website flag), BC-14 (redirect target) | versioning | additive fields; BC-13 adds both so BC-14 does not touch the file |
| `internal/op/getobject.go` | BC-14 | versioning (versionId), writepath (checksum headers) | BC-14 adds one optional hook field and one call; the hook body lives in `internal/op/website_get.go` |
| `internal/op/{putobject,copyobject,uploadpart,initmultipart}.go`, `internal/s3/{putobject,copyobject,multipart}.go` | BC-26 | writepath (checksums, compression, placement), versioning, lock-lifecycle | BC-26 replaces each `bucketEncryptionUnserved` and `refuseEncryption` call with one call into a new `internal/op/encryption.go`; the controller merges BC-26 before or after writepath's tasks, never in parallel with them on the same op |
| `internal/meta/bucket_info.go` | BC-9 | lock-lifecycle (object-lock configuration) | each area changes only its own field's type and its decode/encode line, with the type in its own file (`meta/website.go`, `meta/objlock.go`) |
| `internal/auth/verifier.go` | BC-5 | - | BC-31 adds a new file and does not edit this one |
| `hack/s3tests/deselect-phase2.txt`, `test/s3tests/baseline/*.json`, `known-differences-*.txt` | BC-8, BC-22, BC-27, BC-28f, BC-34 | every area's recording | recording tasks land one at a time through the controller (the ledger is append-only JSON) |
| `docs/exclusions.md`, `docs/ceph-upstream-bugs.md` | nearly every task | every area | PP-5's process fix (per-entry files or a controller merge); not this area's to solve |

### 7.2 Shared seam

**BC-1. Route sets per feature (handler).** Files: `internal/s3/handler.go` (`NewHandler`,
`buildHandler`), new `internal/s3/routesets.go`, stub files `cors.go`, `website.go`,
`publicaccess.go`, `encryption.go`, `policystatus.go`, `ownership.go`, `postobject.go` in
`internal/s3`, each returning an empty set. Seam: `type routeSet struct { scope op.Scope;
handlers map[string]HandlerFunc }`, and `func bucketConfigRoutes(env *op.Env, auth
Authenticator) []routeSet` listing the feature functions, so a feature whose handler needs the
authenticator (POST object) gets it. Behaviour: unchanged; `buildHandler` keeps its panics on
an unknown scope and name and on a double binding, now across sets. Specs: an empty set binds
nothing; a set binding a route twice panics; a set naming a route its scope never dispatches
panics. Depends on nothing. One PR. Brief about 70 lines.

### 7.3 CORS

**BC-2. CORS types (`internal/cors`, new package).** RGWCORSConfiguration and RGWCORSRule:
Encode and Decode at (1,1) (`src/rgw/rgw_cors.h:63-111@v19.2.6`), sets sorted on encode;
`ParseXML` with `xml_end`'s rules and MalformedXML for each refusal
(`src/rgw/rgw_cors_s3.cc:76-188@v19.2.6`), read through `xmltext.Parse`; `MarshalS3XML` in
`to_xml`'s order (`:32-74`, `:166-175`); origin and header matching (`src/rgw/rgw_cors.cc:109-173@v19.2.6`),
expose-header formatting (`:175-184`), request-method flags (`src/rgw/rgw_cors.h:137-147@v19.2.6`);
rule selection as main's `match_rule` (first rule matching origin, method and, for a
preflight, every requested header; `src/rgw/rgw_cors.cc:242-290@cc1dcecee08#RGWCORSConfiguration::match_rule`). Seams:
`Decode(b []byte) (*Configuration, error)`, `(*Configuration).Match(origin, method string,
reqHeaders []string) *Rule`, `(*Rule).ResponseHeaders(origin, method, reqHeaders string,
authorized bool) Headers` (generate_cors_headers' and the preflight's header values, `*` for an
unauthorized request on a wildcard rule). Specs: the corpus goldens for RGWCORSRule; vectors
for a configuration; every xml_end refusal; the match table of test_cors_origin_response; a
configuration where the floors' origin-only choice and main's differ; a 2^32 MaxAgeSeconds; an
expose header with a newline. Depends on nothing. One PR. About 200 lines.

**BC-3. CORS ops (op).** File `internal/op/cors.go`: `GetBucketCORS`, `PutBucketCORS`,
`DeleteBucketCORS`, `OptionsCORS`, each Init `loadBucket`. Get: §2.1 (no attr
`ErrNoCORSFound`, undecodable 500). Put: `Params` hook as PutBucketTagging's; writes only
`user.rgw.cors` through `retryBucketWrite` (defect #4 not reproduced). Delete: s3:PutBucketCORS,
removes the attr, success when absent. OptionsCORS: no permission check; Execute in
`RGWOptionsCORS::execute`'s order (`src/rgw/rgw_op.cc:6174-6204@v19.2.6`) with main's rule
selection; results `Rule`, origin, method, allowed headers. Specs: each op's error order with
fakes; a lost race whose winner changed the ACL keeps that ACL; a retry re-authorizes. Depends
on BC-2. One PR. About 180 lines.

**BC-4. Non-preflight CORS refusal (op).** Files `internal/op/op.go` (one call),
`internal/op/corsmatch.go`. Behaviour: main's `verify_cors_match`
(`src/rgw/rgw_op.cc:2088-2133@292995374d9#RGWOp::verify_cors_match`): after the op mask and
before the permission verdict and the admin override, a request that is not options_cors, has
a non-empty Origin and a loaded bucket whose CORS configuration no rule matches for the
request's own method is 403 AccessDenied; no configuration or no Origin passes; an
undecodable configuration is the 500 radosgw's read gives. Specs: Origin match and mismatch,
the method taken from the request not Access-Control-Request-Method, an admin still refused,
a service-scope request untouched, an op error before the op mask answered first. Depends on
BC-2. One PR. About 100 lines. New exclusion (defect #1).

**BC-5. Preflight signing (auth).** Files `internal/auth/sigv4.go`, `sigv2.go`, `verifier.go`.
Behaviour (§2.1): for an OPTIONS request, the canonical method is
Access-Control-Request-Method, and a missing or unknown one is 400 InvalidArgument at radosgw's
point (after the credentials and canonical headers, before the key lookup), for SigV4 on both
releases and SigV2 on Tentacle only (`src/rgw/rgw_auth_s3.cc:705-732@v19.2.6`,
`src/rgw/rgw_auth_s3.cc:1749-1776@v20.2.4`); once `lookup` succeeds the signature is not compared
(`src/rgw/rgw_rest_s3.cc:6354-6360@v19.2.6`). Retires "A signed OPTIONS request is verified
over OPTIONS and answered 501 until CORS lands"; updates the registry entry's rgw-go line.
Specs: a presigned OPTIONS without the header is 400 before an unknown key's 403; a wrong
signature passes; an unknown key is 403 InvalidAccessKeyId; SigV2 on each release. Depends on
nothing. One PR. About 120 lines.

**BC-6. CORS routes (handler).** File `internal/s3/cors.go`. Behaviour: get_cors (the document,
`application/xml`, body as written by `RGWGetCORS_ObjStore_S3::send_response`), put_cors
(`readParamBody`, `rgw_cors_rules_max_num` with its message, 200 empty), delete_cors (204),
options_cors (200 with §2.1's headers, 403, 400). Specs: status, headers and body of each,
from the s3-tests cases. Depends on BC-1, BC-3. One PR. About 150 lines.

**BC-7. CORS headers on responses (handler).** File `internal/s3/headers.go`. Behaviour:
`SetCommonHeaders` adds the `Access-Control-*` and `Vary` headers from BC-2's
`ResponseHeaders` when the request carries an Origin and its bucket record is loaded with a
configuration, on the responses open question 2 settles; presigned requests get `*` on a
wildcard rule. Specs: GET object 200 and 404 with headers, an auth failure without them (no
bucket loaded), a presigned request, a non-matching method. Depends on BC-2. One PR. About 120
lines.

**BC-8. CORS recording and gate (cluster).** Delete deselect lines 92-108, record both ledgers,
add known-differences lines for defects #1 and #2 under their exclusions, run gate case 1, and
reproduce #1, #2 and #4 for the registry. Depends on BC-3 to BC-7. One PR per release's
recording. About 80 lines.

### 7.4 Static website

**BC-9. Website configuration type (meta).** Files new `internal/meta/website.go`;
`internal/meta/bucket_info.go`, `bucket_json.go`, `dump_bucket.go`. Behaviour:
RGWBucketWebsiteConf and its sub-structs (`src/rgw/rgw_website.h:26-243@v19.2.6`), decoded at
v1 and v2 and encoded at v2, so a v1 configuration is rewritten as v2 like radosgw's; the
bucket instance's JSON `website_conf` dump and decode as radosgw's lossy form
(`src/rgw/rgw_website.cc:135-212@v19.2.6`). `BucketInfo.Website` becomes `*WebsiteConf`; the
JSON refusal stays for sync policy only, and the admin metadata API's website 501 is retired.
Specs: v1 and v2 vectors; an RGWBucketInfo with a website round-tripping against a captured
radosgw instance; the JSON dump of each shape. Depends on nothing. One PR. About 200 lines.

**BC-10. Website semantics and XML (`internal/website`, new package).** Behaviour: PutBucketWebsite's
validation and messages (`src/rgw/rgw_rest_s3.cc:2271-2337@v19.2.6`), `decode_xml` and
`dump_xml` (`src/rgw/rgw_website.cc:214-341@v19.2.6`), `get_effective_key`, `should_redirect`
and `apply_rule` (`:34-133`). The error code of an unparseable body follows open question 3.
Seams: `ParseXML(body []byte, maxRules int) (meta.WebsiteConf, error)`, `MarshalS3XML`,
`EffectiveKey(conf, key string, isFile bool) (string, bool)`, `Redirect(conf, key string,
status int, proto, host string) (url string, code int, ok bool)`. Specs: tables from each cited
function, the redirect-code bounds, the routing-rule limit. Depends on BC-9. One PR. About 200
lines.

**BC-11. Website configuration ops (op).** File `internal/op/website.go`: Get, Set and Delete
per §2.2, the writes through `PutBucketInfo` under `RetryRacedBucketWrite` with
re-authorization. Specs: error order, a lost race, a v1 configuration rewritten. Depends on
BC-9. One PR. About 120 lines.

**BC-12. Website configuration routes (handler).** File `internal/s3/website.go`. Behaviour:
the three routes, 405 MethodNotAllowed while `rgw_enable_static_website` is off, the documents
and statuses of §2.2. Depends on BC-1, BC-10, BC-11. One PR. About 110 lines.

**BC-13. Website requests (request parsing).** Files `internal/cephconf/apis.go`,
`internal/vhost/vhost.go`, `internal/s3/request.go`, `internal/op/request.go`,
`internal/s3/multipart.go` (`completeLocation`'s domain). Behaviour: `EnabledAPIs` reports
`s3website` and its priority over `s3`; the s3website hostnames (`rgw_dns_s3website_name` and
the zonegroup's `hostnames_s3website`) join the virtual-host match; a request is a website
request as `RGWREST::preprocess` decides (`src/rgw/rgw_rest.cc:2017-2179@v19.2.6#RGWREST::preprocess`),
with v20.2.4's no-Host branch on Tentacle only; `op.Request` gains `Website bool` and
`Redirect string`; CompleteMultipartUpload's Location takes the s3website domain as
`compute_domain_uri` does. Updates the two exclusions §3 names. Specs: each host form against
each `rgw_enable_apis` order, both releases. Depends on nothing. PR split: (a) cephconf and
vhost, (b) request flag and Location. About 200 lines.

**BC-14. Website retarget (op).** Files `internal/op/getobject.go` (one hook), new
`internal/op/website_get.go`. Behaviour: `retarget` between the bucket load and the object
read (`src/rgw/rgw_rest_s3.cc:5357-5418@v19.2.6#RGWHandler_REST_S3Website::retarget`), its
`web_dir` stat (`:5316-5340`), redirects as `ErrWebsiteRedirect` with `r.Redirect` and the
rule's status, and the `x-amz-website-redirect-location` 301
(`src/rgw/rgw_rest_s3.cc:266-284@v19.2.6`). Specs: each effective-key case, a directory
object, a redirect-all, a routing rule with and without a code, a redirect attr. Depends on
BC-10, BC-13. One PR. About 180 lines.

**BC-15. Website serving and errors (handler).** Files new `internal/s3/website_serve.go`,
`internal/s3/html.go`; `internal/s3/dispatch.go`, `handler.go`, `errors.go`. Behaviour:
website dispatch (GET and HEAD to the website GetObject at every scope, everything else 405);
HTML error documents for every website-mode refusal, as ceph's HTMLFormatter writes them; the
error handler: a routing rule on the original key and the HTTP status redirects, else the error
document by a nested GetObject with conditionals cleared and the original status, else the HTML
page (`src/rgw/rgw_rest_s3.cc:5430-5555@v19.2.6`); Location on WebsiteRedirect. Specs:
statuses, headers and bodies for each path; an error document that is itself missing falls back
to the HTML page. Depends on BC-12, BC-14. PR split: (a) the HTML renderer and website-mode
refusals, (b) serving and the error handler. About 230 lines.

**BC-16. Website gate (cluster).** Gate case 2, both directions, both releases, the v1 rewrite
included; the admin metadata JSON compared with radosgw-admin's. No s3-tests exist. Depends on
BC-9 to BC-15. One PR. About 70 lines.

### 7.5 Public access block and policy status

**BC-17. Block XML (acl).** File `internal/acl/publicaccess.go`. Behaviour:
`PublicAccessBlockConfiguration::decode_xml` and `dump_xml`
(`src/rgw/rgw_public_access.cc:4-20@v19.2.6`), the namespace per open question 3. Specs: each
boolean optional, the rendered document. Depends on nothing. One PR. About 70 lines.

**BC-18. Block ops (op).** File `internal/op/publicaccess.go`: Get, Put and Delete per §2.5,
Put writing only `user.rgw.public-access` (defect #4), Get answering an undecodable block 500.
Phase 1's enforcement picks the attr up with no change. Specs: error order, a lost race, a
retry re-authorized. Depends on BC-17. One PR. About 120 lines.

**BC-19. Block routes (handler).** File `internal/s3/publicaccess.go`: 200 empty, the
document, 204; a 404 that sends the error document alone (defect #6). Depends on BC-1, BC-18.
One PR. About 90 lines.

**BC-20. Policy status op (op).** File `internal/op/policystatus.go`: IsPublic from
`policy.Policy.IsPublic` with the semantics open question 4 fixes, or `acl.Policy.IsPublic`
(`src/rgw/rgw_op.cc:8582-8598@v19.2.6`); a stored policy that does not parse is the
authorizer's refusal before this. Specs: each s3-test's bucket shape. Depends on nothing. One
PR. About 70 lines.

**BC-21. Policy status route (handler).** File `internal/s3/policystatus.go`: `PolicyStatus`
with lower-case booleans (`src/rgw/rgw_rest_s3.cc:4554-4572@v19.2.6`). Depends on BC-1, BC-20.
One PR. About 60 lines.

**BC-22. Block and policy-status recording and gate (cluster).** Delete deselect lines 111-128,
record both ledgers, gate cases 3 and 5, reproduce defect #6 from the raw bytes. Depends on
BC-17 to BC-21. One PR. About 70 lines.

### 7.6 Encryption configuration

**BC-23. Configuration type (`internal/sse`, new package, which phase 4's SSE-C joins).**
Behaviour: the three structs at (1,1) (`src/rgw/rgw_bucket_encryption.h:9-141@v19.2.6`), the XML of
`src/rgw/rgw_bucket_encryption.cc:8-40@v19.2.6`, and the validation open question 6 settles. Specs:
the four corpus goldens; XML both ways; each validation refusal. Depends on nothing. One PR.
About 150 lines.

**BC-24. Configuration ops (op).** File `internal/op/bucketencryption.go`: §2.3's three ops;
Delete removes `user.rgw.sse-s3.policy` and `user.rgw.sse-s3.key-id`. Depends on BC-23. One PR.
About 110 lines.

**BC-25. Configuration routes (handler).** File `internal/s3/encryption.go`: 200 empty, the
document, 204. Depends on BC-1, BC-24. One PR. About 80 lines.

**BC-26. The default on writes (op and s3 write path).** Files new `internal/op/encryption.go`;
the call sites of §3. Behaviour: `get_encryption_defaults` (`src/rgw/rgw_rest_s3.cc:146-264@v19.2.6#get_encryption_defaults`)
then the unconfigured `rgw_s3_prepare_encrypt` (`src/rgw/rgw_crypt.cc:1030-1301@v19.2.6`):
the conflict and decode refusals, `rgw_crypt_require_ssl` against the transport, and the
no-backend failure recorded by BC-27; SSE-C keeps 501 until phase 4; a configuration naming no
algorithm refuses nothing; the default's algorithm and key id enter the policy environment
before authorization. The brief re-derives CopyObject's and UploadPart's paths at both tags
before it changes them. Retires `bucketEncryptionUnserved` and the SSE-S3/KMS half of
`refuseEncryption` and the two exclusions §3 names. Seam: `func EncryptionDefaults(r *Request,
asked map[string]string) (Encryption, error)` for PutObject, CreateMultipartUpload and POST
object. Specs: every refusal of the two functions, TLS and plain HTTP, the policy keys. Depends
on BC-23. PR split: (a) helper with PutObject and CreateMultipartUpload, (b) CopyObject and
the part uploads. About 220 lines.

**BC-27. Encryption recording and gate (cluster).** Record the six configuration tests (needs
open question 7's include list), gate case 4, and radosgw's write answers on an unconfigured
zone over TLS and plain HTTP, which BC-26's specs then pin. Depends on BC-23 to BC-26. One
PR. About 70 lines.

### 7.7 Ownership controls (option A of open question 1)

**BC-28a. Type (acl).** New `internal/acl/ownership.go`: v21's enum and (1,1) encoding, XML and
the missing-or-undecodable rule (`src/rgw/rgw_object_ownership.cc:77-106@v21.1.2`). About 90
lines. **BC-28b. Dispatch (s3).** `ownershipControls` rows for GET, PUT and DELETE ahead of the
fall-through, with v21's payload forms; until BC-28d they answer 501. About 60 lines.
**BC-28c. Ops (op).** New `internal/op/ownership.go`: v21's three ops
(`src/rgw/rgw_op.cc:10247-10358@v21.1.2`) and CreateBucket's `x-amz-object-ownership`. About
150 lines. **BC-28d. Routes (handler).** About 80 lines. **BC-28e. Enforcement (authz and
write path).** v21's refusal of ACLs under BucketOwnerEnforced
(`src/rgw/rgw_op.cc:6868-6869@v21.1.2`), ACLs ignored in evaluation under it, and the object
owner for BucketOwnerEnforced and BucketOwnerPreferred, each re-derived from v21.1.2 in the
brief. About 230 lines; PR split by ACL ops, evaluation, object owner. **BC-28f. Recording
(cluster).** The 8 tests leave EXCLUDED_MARKERS, enter known-differences, gate case 7. About
60 lines. Option B replaces all six with one dispatch task: `ownershipControls` answers 501
NotImplemented for every method, about 50 lines.

### 7.8 POST object

**BC-29. Form reader (s3).** New `internal/s3/postform.go`. Behaviour: radosgw's reader
(`src/rgw/rgw_rest.cc:1107-1469@v19.2.6`), streaming: boundary parameters, part headers,
case-insensitive part names, trimmed values, the file part as a reader ending at its boundary,
the parts after it drained, line reads bounded by `rgw_max_chunk_size`. Seam: `func
newFormReader(body io.Reader, contentType string, maxLine int) (*formReader, error)`,
`(*formReader).Fields() (formFields, *formFile, error)`, `(*formReader).Drain() error`. Specs:
the framing cases of §5.3 and each "Malformed request" or "Couldn't find boundary". Depends on
nothing. One PR. About 200 lines.

**BC-30. POST policy document (`internal/postpolicy`, new package).** Behaviour:
`RGWPolicy::from_json`, `add_condition`, `check` and `RGWPolicyEnv` with every message
(`src/rgw/rgw_policy_s3.cc:16-305@v19.2.6`), Tentacle's checksum-field exception
(`src/rgw/rgw_policy_s3.cc:108-130@v20.2.4`) taken on both releases with the writepath area's checksum decision; the
JSON read through Go's decoder with ceph's JSONParser behaviour pinned for duplicate keys and
number text. Seams: `Parse(doc []byte) (*Policy, error)`, `(*Policy).Check(env *Env, now
time.Time) error`, `Policy.MinLength`, `MaxLength`. Specs: every s3-test policy shape and every
message. Depends on nothing. One PR. About 180 lines.

**BC-31. Browser-upload credentials (auth).** New `internal/auth/browserupload.go`;
`internal/s3/auth.go` (the interface). Behaviour: §2.7's SigV2 and SigV4 verification
(`src/rgw/rgw_rest_s3.cc:6117-6171@v19.2.6`), the lookup and identity of the header path, a
session token as the header path treats one. Seam: `op.BrowserUploadCredentials{Algorithm,
AccessKey, Credential, Signature, SecurityToken, Policy string}` and `Authenticator.AuthenticateBrowserUpload(ctx,
op.BrowserUploadCredentials) (*op.AuthResult, error)`. Specs: SigV2 vectors, AWS's SigV4 POST
example, a bad key, a bad signature, an inactive key. Depends on nothing. One PR. About 150
lines.

**BC-32. POST object op (op).** New `internal/op/postobject.go`. Behaviour: Init loads the
bucket and runs the handler's Params hook (the form, the credentials, the identity), then
refuses a suspended user (defect #8); VerifyPermission is PutObject's s3:PutObject check on
the key with the form's encryption fields; Execute in `RGWPostObj::execute`'s order
(`src/rgw/rgw_op.cc:4612-4796@v19.2.6`): BlockPublicAcls on the canned ACL (defect #9),
quota, Content-MD5, BC-26's encryption, the writepath area's Tentacle checksum, the versioned
write the versioning area provides (501 until then, as PutObject), `ObjectStore.PutObject` with
the policy's length bounds, the attrs. Specs: error order with fakes, each bound, each attr.
Depends on BC-26, BC-30, BC-31. One PR. About 230 lines.

**BC-33. POST object routes (handler).** New `internal/s3/postobject.go`; `internal/s3/handler.go`
(the byte counter). Behaviour: Content-Type and boundary, Content-Length and
`rgw_max_put_size` in `verify_params`' order, the form through BC-29, the response of
`src/rgw/rgw_rest_s3.cc:3297-3412@v19.2.6` (204 with ETag, 201 PostResponse with a
url-encoded Location, 303, 200), radosgw's messages, the usage log counting the body (defect
#10). Specs: each status and header, `${filename}`, a tenant's redirect. Depends on BC-1,
BC-29, BC-32. One PR. About 220 lines.

**BC-34. POST recording and gate (cluster).** Delete deselect lines 55-89, record both ledgers,
gate case 6, reproduce defects #8, #9 and #10. Depends on BC-29 to BC-33. One PR. About 70
lines.

### 7.9 Order

Wave 1, in parallel: BC-1, BC-2, BC-5, BC-9, BC-13, BC-17, BC-20, BC-23, BC-29, BC-30, BC-31
(and BC-28a). Wave 2: BC-3, BC-4, BC-7, BC-10, BC-11, BC-18, BC-24, BC-26 (after writepath's
tasks on the same ops, per 7.1). Wave 3: BC-6, BC-12, BC-14, BC-19, BC-21, BC-25, BC-32.
Wave 4: BC-15, BC-33, the BC-28 chain. Wave 5, one at a time: BC-8, BC-16, BC-22, BC-27,
BC-34, BC-28f.

## 8. Open questions

1. **Ownership controls exist at neither floor; what does phase 2 serve?** Option A: v21's
   feature, its stored form (`user.rgw.ownership-controls`) and its enforcement, as BC-28a to
   BC-28f. Option B: `ownershipControls` answers 501 for every method. **Recommended: A.** The
   spec names the item, v21 fixes a stored form that an upgraded zone's radosgw will read, it
   is the AWS behaviour the owner's rule points to, and rgw-go already enforces a newer
   release's check on Squid (RestrictPublicBuckets). Its cost is six tasks, one of them in
   authz, and an exclusion: a floor radosgw ignores the attr. If phase 2 must stay smaller, B
   still removes the floors' hazard of deleting the bucket.
2. **Which responses carry CORS headers?** radosgw adds them where `end_header` is passed the
   op (50 calls at v19.2.6, 52 at v20.2.4) and on errors after the bucket loads. AWS documents
   the evaluation per request and names no operation that omits the headers (not verified
   against AWS itself). **Recommended: every S3 response once the request's bucket is
   loaded**, the documented AWS model: no per-release route table to keep, and the 15 routes
   radosgw omits (configuration writes, ListBuckets, CreateBucket) gain headers no client
   relies on lacking. One exclusion lists them.
3. **How far does "do the correct S3 thing" reach for error codes and cosmetics?** Cases here:
   PutBucketWebsite's InvalidArgument for an unparseable body (AWS: MalformedXML); the PAB
   document without the S3 namespace; lower-case booleans. **Recommended: follow AWS where the
   status or error code a client sees differs; keep radosgw's form where only document bytes
   differ that SDKs ignore**, so the cross-gateway oracle stays byte-comparable. One rule from
   the owner settles every area's instances.
4. **`is_public` under the new rule.** PolicyStatus, BlockPublicPolicy and RestrictPublicBuckets
   share it; rgw-go follows Squid's over-block on Squid and reproduces the three-key-environment
   bypass on both (registry). **Recommended: one AWS-correct judgment on both releases, decided
   and built by an authz task outside this area**; BC-20 consumes whatever `Policy.IsPublic`
   then says.
5. **Take radosgw's skip of a signed preflight's signature?** **Recommended: yes**, since AWS
   checks none either and the presigned CORS tests need it; record now that phase 3's rate-limit
   enforcement does not charge OPTIONS CORS to the key's user, the security half of that
   registry entry.
6. **Validate PutBucketEncryption?** The floors store any algorithm. **Recommended: validate as
   AWS documents** (SSEAlgorithm one of `AES256`, `aws:kms`, `aws:kms:dsse`, `aws:fsx`,
   `aws:backup`; KMSMasterKeyID only with `aws:kms` or `aws:kms:dsse`), 400 MalformedXML
   otherwise; the stored form is unchanged, and a write into a bucket whose default neither
   gateway can serve fails on both as on an unconfigured radosgw. This rewrites item 2 of the
   key-management exclusion, which says "the configuration round-trips".
7. **The s3-tests harness cannot run a marker-excluded test this phase serves.** The six
   encryption-configuration tests share their markers with the SSE write tests that stay
   excluded, and the eight ownership tests carry `object_ownership`. **Recommended: an include
   list beside the deselect lists (`hack/s3tests/include-phase2.txt`), read by run.sh, owned by
   the harness's area**; the alternative, dropping markers and deselecting the SSE tests by
   node id, lists 65 ids (the 71 tests the three encryption markers carry in test_s3.py, less the
   six).
