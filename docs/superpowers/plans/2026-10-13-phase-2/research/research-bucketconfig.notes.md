# Notes: `bucketconfig` research

Verified facts, each with its source and release. Appended as verified.

## Inputs read (2026-10-13)

- origin/main is 0239625 (`git rev-parse origin/main` = 02396257d1d8). The phase-1-exec
  worktree HEAD is af535e4, behind main; every rgw-go fact below is read from an
  origin/main snapshot (`git archive origin/main`), not the worktree.
- s3-tests PIN `hack/s3tests/PIN` = f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3; present in
  /home/jhoblitt/github/ceph. Functional files at the pin: test_s3.py, test_headers.py,
  test_iam.py, test_s3control.py, test_s3select.py, test_sns.py, test_sts.py. No
  test_s3website.py: website tests live in test_s3.py under the s3website markers.
- run.sh `EXCLUDED_MARKERS` (hack/s3tests/run.sh:66-72 @0239625) names, for this area:
  object_ownership, encryption, bucket_encryption, sse_s3, s3website,
  s3website_routing_rules, s3website_redirect_location. TEST_FILES = test_s3.py,
  test_headers.py only (:73).
- deselect-phase2.txt @0239625: POST object lines 55-89 (35 tests); CORS lines 92-108
  (17 tests: 2 test_object_raw_get_x_amz_expires_not_expired{,_tenant}, test_set_cors,
  test_cors_origin_response, test_cors_origin_wildcard, test_cors_header_option, and the 11
  test_cors_presigned_*); public access block lines 111-120 (10); policy status 123-128 (6).
- Baselines test/s3tests/baseline/{squid,tentacle}.json @0239625 hold 416 tests each and
  none of this area's deselected or marker-excluded tests: radosgw's outcome for them is
  NOT recorded at either floor. Recording them is part of phase 2's work.
- s3-tests markers at the pin (test_s3.py): object_ownership 8 tests; bucket_encryption 12;
  sse_s3 21; encryption 68; fails_on_rgw includes test_cors_presigned_get_object_v2,
  test_cors_presigned_get_object_tenant_v2, test_cors_presigned_put_object_v2,
  test_cors_presigned_put_object_tenant_v2.

## rgw-go at origin/main 0239625

- Dispatch table internal/s3/dispatch.go:120-250 already routes, at bucket scope:
  GET website/cors/policyStatus/publicAccessBlock/encryption (:132,:135,:144-146);
  PUT website/cors/publicAccessBlock/encryption (:162,:165,:172-173); DELETE
  cors/publicAccessBlock/encryption/website (:181,:186-188); POST default post_obj (:199);
  OPTIONS options_cors (:201). Object scope: POST default post_obj (:247), OPTIONS
  options_cors (:249). No ownershipControls row: it falls through to the method default
  (list_bucket / create_bucket / delete_bucket).
- payloadTable dispatch.go:309-345 already lists put_cors, put/get/delete_bucket_encryption,
  set/delete_bucket_website, put/get/delete_bucket_public_access_block as PayloadSigned.
  Not listed (no signed payload accepted): get_cors, delete_cors, get_bucket_website,
  get_bucket_policy_status, post_obj, options_cors.
- Handler: a dispatched route with no bound HandlerFunc answers 501 NotImplemented after
  auth, PostAuthInit and the suspended check (internal/s3/handler.go:377-381).
- bucketHandlers internal/s3/bucket.go:22-41 binds none of this area's routes.
- acl.PublicAccessBlock + Encode/Decode exist (internal/acl/publicaccess.go:9-38), comment
  "rgw-go does not manage it yet, but honors a stored one."
- op.PutBucketPolicy refuses a public policy under BlockPublicPolicy per try
  (internal/op/bucketsubres.go:333-363); PutBucketACL refuses a public ACL under
  BlockPublicAcls (:171-199).

## Ownership controls (verified 2026-10-13)

- Neither floor implements ownership controls. At v19.2.6 and v20.2.4 the only mentions are
  the IAM action names s3:GetBucketOwnershipControls / s3:PutBucketOwnershipControls
  (`src/rgw/rgw_iam_policy.cc:84`, `:117`@v19.2.6; `:84`, `:120`@v20.2.4;
  `rgw_iam_policy.h:68-69` at both). No `ownershipControls` op branch, no
  x-amz-object-ownership handling.
- `ownershipControls` is not among RGWHTTPArgs::append's sub-resources
  (`src/rgw/rgw_common.cc:918-976@v19.2.6`). The bucket op_get/op_put/op_delete have no
  branch for it, so radosgw serves GET ?ownershipControls as ListObjects, PUT as
  CreateBucket, DELETE as DeleteBucket (rgw-go's dispatch already falls through the same way:
  internal/s3/dispatch.go:127-192 @0239625).
- Ownership controls first appear in ceph commit b3733296c8e (2025-06-24, "rgw/s3: add
  ObjectOwnership and OwnershipControls"); `git tag --contains` lists v21.0.0 and later
  only. Main stores it in a separate bucket attr `user.rgw.ownership-controls`
  (main:src/rgw/rgw_common.h:126), errors ERR_INVALID_BUCKET_ACL 2228 (400
  InvalidBucketAclWithObjectOwnership), ERR_NO_SUCH_OWNERSHIP_CONTROLS 2229 (404
  OwnershipControlsNotFoundError) (main:rgw_common.h:364-365).
- s3-tests at the pin: 8 object_ownership tests (test_s3.py:21557-21662 at f6f2529):
  test_create_bucket_no_ownership_controls expects 404 OwnershipControlsNotFoundError;
  test_bucket_create_delete_bucket_ownership calls DeleteBucketOwnershipControls twice on an
  empty bucket (on a floor radosgw that is DeleteBucket).

## CORS (verified 2026-10-13)

- Helpers identical at both tags (fn.py diff): validate_cors_rule_method
  `src/rgw/rgw_op.cc:1451-1467@v19.2.6` / `src/rgw/rgw_op.cc:1688-1704@v20.2.4#validate_cors_rule_method`; validate_cors_rule_header
  `:1469-1481` / `:1706-1718`; RGWOp::read_bucket_cors `:1483-1512` / `:1720-1749` (no
  `user.rgw.cors` attr -> cors_exist=false, 0; decode failure -> -EIO);
  get_cors_response_headers `:1519-1534` / `:1756-1771`; RGWOp::generate_cors_headers
  `:1541-1600` / `:1778-1837` (needs HTTP_ORIGIN; host_name_rule; "*" only when no
  HTTP_AUTHORIZATION and the rule has a wildcard origin; method from
  Access-Control-Request-Method else the request method; rule must allow it; headers
  filtered by the rule; exp headers and max age from the rule).
- CORS ops at both tags, identical except the forward call's extra s->err argument:
  RGWGetCORS::verify_permission `src/rgw/rgw_op.cc:6046-6057@v19.2.6#RGWGetCORS::verify_permission` / `src/rgw/rgw_op.cc:6716-6727@v20.2.4#RGWGetCORS::verify_permission`
  (s3:GetBucketCORS, adds bucket tags if a policy names s3:ResourceTag);
  RGWGetCORS::execute `:6059-6070` / `:6729-6740` (no attr -> -ERR_NO_CORS_FOUND);
  RGWPutCORS::verify_permission `:6072-6083` / `:6742-6753` (s3:PutBucketCORS);
  RGWPutCORS::execute `:6085-6105` / `:6755-6775` (get_params, forward, retry_raced_bucket_write
  merging s->bucket_attrs with user.rgw.cors = cors_bl);
  RGWDeleteCORS::verify_permission `:6107-6119` / `:6777-6789` (s3:PutBucketCORS, "No
  separate delete permission"); RGWDeleteCORS::execute `:6121-6150` / `:6791-6820` (no CORS
  -> -ENOENT; erases attr from s->bucket->get_attrs() and put_info);
  RGWOptionsCORS::validate_cors_request `:6156-6172` / `:6826-6842`;
  RGWOptionsCORS::execute `:6174-6204` / `:6844-6874` (decode error first; no Origin ->
  -EINVAL; no Access-Control-Request-Method -> -EINVAL; no CORS -> -ENOENT; rule match).
  RGWOptionsCORS::verify_permission returns 0 (`src/rgw/rgw_op.h:1752@v19.2.6`).
  Op types: get_cors RGW_OP_GET_CORS read; put_cors write; delete_cors write; options_cors
  RGW_OP_OPTIONS_CORS read (`src/rgw/rgw_op.h:1693-1760@v19.2.6`).
- S3 REST layer, identical at both tags: RGWGetCORS_ObjStore_S3::send_response
  `src/rgw/rgw_rest_s3.cc:3719-3740@v19.2.6` / `src/rgw/rgw_rest_s3.cc:4200-4221@v20.2.4#RGWGetCORS_ObjStore_S3::send_response` (end_header with
  op=NULL; body = RGWCORSConfiguration_S3::to_xml); RGWPutCORS_ObjStore_S3::get_params
  `:3742-3802` / `:4223-4283` (read_all_input rgw_max_put_param_size; parse failure or no
  CORSConfiguration root -> MalformedXML; rules > rgw_cors_rules_max_num (negative ->
  100) -> ERR_INVALID_CORS_RULES_ERROR with message "The number of CORS rules should not
  exceed allowed limit of N rules."); RGWPutCORS_ObjStore_S3::send_response `:3804-3811` /
  `:4285-4292` (200, op NULL, content type from format); RGWDeleteCORS_ObjStore_S3::send_response
  `:3813-3822` / `:4294-4303` (0 or -ENOENT -> 204); RGWOptionsCORS_ObjStore_S3::send_response
  `:3824-3845` / `:4305-4326` (-ENOENT -> -EACCES -> 403; success dump_access_control with
  origin, req_meth, allowed headers, expose headers, max age; op=NULL).
- dump_access_control(origin...) `src/rgw/rgw_rest.cc:526-554@v19.2.6`: ACAO; Vary: Origin
  when origin != "*"; ACAM, ACAH, ACEH when non-empty; Access-Control-Max-Age when not
  CORS_MAX_AGE_INVALID. dump_access_control(s, op) `:556-569`. end_header calls it when op
  is non-null (`:589-655`, the call at `:604`). abort_early passes the op it has
  (`:676-743`, end_header calls at `:738`, `:741`).
- end_header calls that pass the op (CORS headers possible) at v19.2.6 rgw_rest_s3.cc: 50
  sites; v20.2.4: 52 (a one-off scan of every end_header call; the scratch script is deleted). NOT passing an op at v19.2.6 include:
  ListBuckets send_response_begin (:1519), CreateBucket (:2551), Get/Put/Delete/Options
  CORS (:3728, :3809, :3821, :3836/:3844), PutBucketEncryption (:3853),
  DeleteBucketEncryption (:3884), SetRequestPayment (:3966), GetBucketMetaSearch,
  PutBucketObjectLock, PutObjRetention, PutObjLegalHold, PutBucketPublicAccessBlock (:4580).
  Passing: GetObj, object/bucket tagging ops, ListBucket v1/v2, GetBucketLocation,
  versioning, website get/set/delete, StatBucket, DeleteBucket, PutObj, PostObj, DeleteObj,
  CopyObj partial response, Get/PutACLs, LC get/put/delete, GetBucketEncryption,
  GetRequestPayment, multipart ops, DeleteMultiObj begin, GetObjLayout, object lock gets,
  GetBucketPolicyStatus, GetBucketPublicAccessBlock; rgw_rest.cc abort_early (:738,:741),
  RGWRESTFlusher::do_start (:1040), RGWRESTOp::send_response (:1682).
- process_request order at v19.2.6 (`src/rgw/rgw_process.cc:280-420`): get_handler, get_op,
  schedule, verify_requester (abort_early with op), postauth_init, suspended,
  rgw_process_authenticated (`:165-260`: init_permissions loads the bucket and its attrs,
  retarget (website), read_permissions, init_processing, verify_op_mask, verify_permission
  with system/admin override, verify_params, pre_exec, rate limit, execute, complete).

## Bucket encryption configuration (verified 2026-10-13)

- RGWPutBucketEncryption::get_params `src/rgw/rgw_op.cc:8744-8749@v19.2.6` /
  `src/rgw/rgw_op.cc:9725-9730@v20.2.4#RGWPutBucketEncryption::get_params`; verify_permission `:8751-8757` / `:9732-9738` (s3:PutBucketEncryption,
  no tag loading); execute `:8759-8799` / `:9740-9780`: parser init failure -EINVAL; body read;
  parse failure MalformedXML; decode_xml("ServerSideEncryptionConfiguration", mandatory)
  failure MalformedXML; forward; encode; retry_raced_bucket_write merging
  s->bucket->get_attrs() with user.rgw.sse-s3.policy. No validation of SSEAlgorithm or key id
  and no backend check: an unconfigured radosgw stores any algorithm string.
- RGWGetBucketEncryption::verify_permission `:8801-8807` / `:9782-9788`
  (s3:GetBucketEncryption); execute `:8809-8828` / `:9790-9809`: no attr -> -ENOENT with
  message "The server side encryption configuration was not found" (only the log level
  differs: 0 at v19.2.6, 20 at v20.2.4); decode failure -> -EIO.
- RGWDeleteBucketEncryption::verify_permission `:8830-8836` / `:9811-9817`
  (s3:PutBucketEncryption); execute `:8838-8854` / `:9819-9835`: erases
  user.rgw.sse-s3.policy AND user.rgw.sse-s3.key-id (RGW_ATTR_BUCKET_ENCRYPTION_KEY_ID),
  put_info; succeeds with nothing to remove.
- S3 REST at v19.2.6: RGWPutBucketEncryption_ObjStore_S3::send_response
  `src/rgw/rgw_rest_s3.cc:3847-3854` (200, end_header(s): no op, no CORS);
  RGWGetBucketEncryption_ObjStore_S3::send_response `:3856-3874` (-ENOENT ->
  ERR_NO_SUCH_BUCKET_ENCRYPTION_CONFIGURATION; op passed; body
  encode_xml("ServerSideEncryptionConfiguration", XMLNS_AWS_S3, ...));
  RGWDeleteBucketEncryption_ObjStore_S3::send_response `:3876-3885` (0 -> 204; no op).
- Types identical at both tags (diff empty): `src/rgw/rgw_bucket_encryption.h` -
  ApplyServerSideEncryptionByDefault ENCODE_START(1,1){kmsMasterKeyID, sseAlgorithm};
  ServerSideEncryptionConfiguration ENCODE_START(1,1){apply, bucketKeyEnabled};
  RGWBucketEncryptionConfig ENCODE_START(1,1){rule_exist, rule if rule_exist}. XML:
  `rgw_bucket_encryption.cc` decode_xml KMSMasterKeyID, SSEAlgorithm (optional),
  ApplyServerSideEncryptionByDefault, BucketKeyEnabled (optional), Rule (optional);
  dump_xml writes SSEAlgorithm then KMSMasterKeyID only when non-empty;
  BucketKeyEnabled only when true; Rule only when rule_exist.
- get_encryption_defaults `src/rgw/rgw_rest_s3.cc:146-264@v19.2.6` (`src/rgw/rgw_rest_s3.cc:151-269@v20.2.4#get_encryption_defaults`):
  SSE-C plus SSE-KMS/S3 headers -> -EINVAL; stored config that does not decode -> -EINVAL
  "Server side error - can't decode bucket_encryption_conf"; SSE-C wins; a stored algorithm
  fills x-amz-server-side-encryption when the request names none; kms key id, bucket-key
  and context only valid with a REST algorithm; kms attrs with no algorithm -> aws:kms;
  kms attrs with another algorithm -> -EINVAL. Callers: PutObj get_params `src/rgw/rgw_rest_s3.cc:2612@v19.2.6#RGWPutObj_ObjStore_S3::get_params`
  (`src/rgw/rgw_rest_s3.cc:2773@v20.2.4#RGWPutObj_ObjStore_S3::get_params`), PostObj get_params `:2973` (`:3118`), InitMultipart get_params `:3973`
  (`:4454`).
- rgw_s3_prepare_encrypt `src/rgw/rgw_crypt.cc:1030-1301@v19.2.6`: for
  x-amz-server-side-encryption present: rgw_crypt_require_ssl (default true) and an
  insecure transport -> -ERR_INVALID_REQUEST; aws:kms without key id -> -EINVAL; aws:kms ->
  make_actual_key_from_kms; neither aws:kms nor AES256 -> -EINVAL; AES256 with
  rgw_crypt_sse_s3_backend != "vault" -> -EINVAL "Request specifies Server Side
  Encryption but server configuration does not support this."; AES256 -> get_sse_s3_bucket_key
  (`:983-1028`) -> create_sse_s3_bucket_key (`src/rgw/rgw_kms.cc:1249-1272@v19.2.6`).
- Defaults at both tags (`src/common/options/rgw.yaml.in`): rgw_crypt_require_ssl true
  (`src/common/options/rgw.yaml.in:2817-2821@v19.2.6`, `src/common/options/rgw.yaml.in:2955-2959@v20.2.4`); rgw_crypt_s3_kms_backend barbican
  (`:2832-2840`, `:2970-2978`); rgw_crypt_sse_s3_backend vault (`:3039-3046`, `:3177-3184`).
  Unconfigured KMS path: barbican -> get_keystone_barbican_token failure -> -EINVAL
  (`src/rgw/rgw_kms.cc:961-979@v19.2.6`), or rgw_barbican_url empty -> -EINVAL (`:919-959`).
  Vault path with empty address -> -EINVAL (`:267-271`). The exact final status of an SSE-S3
  or SSE-KMS write on an unconfigured radosgw is therefore -EINVAL (400 InvalidArgument)
  over TLS and 400 InvalidRequest over plain HTTP, BY SOURCE READING ONLY: the keystone
  token path was not traced to its end. Unreproduced; record from a running radosgw.
- DeleteBucket calls rgw_remove_sse_s3_bucket_key for the owner
  (`src/rgw/rgw_op.cc:3785-3790@v19.2.6`, `src/rgw/rgw_op.cc:3995@v20.2.4`); it returns 0 when the bucket has
  no user.rgw.sse-s3.key-id attr (`src/rgw/rgw_crypt.cc:1507-1538@v19.2.6`).
- s3-tests config-op tests at the pin: test_put/get/delete_bucket_encryption_s3 (sse_s3)
  and _kms (encryption) at test_s3.py:15343-15435; Delete on a bucket without a
  configuration expects 204; Get without one expects
  ServerSideEncryptionConfigurationNotFoundError.

## CORS types and parsing (verified 2026-10-13)

- rgw_cors.h, rgw_cors.cc, rgw_cors_s3.h, rgw_cors_s3.cc are byte-identical between v19.2.6
  and v20.2.4 (diff empty).
- RGWCORSRule `src/rgw/rgw_cors.h:37-92@v19.2.6`: ENCODE_START(1,1){max_age u32,
  allowed_methods u8, id, allowed_hdrs set<string>, allowed_origins set<string>,
  exposable_hdrs list<string>} (`:63-82`). RGWCORSConfiguration ENCODE_START(1,1){rules
  list} (`:102-111`). Method flags GET 0x1 PUT 0x2 HEAD 0x4 POST 0x8 DELETE 0x10 COPY 0x20
  (`:22-33`); CORS_MAX_AGE_INVALID = 0xffffffff (`:35`). validate_name_string: empty or more
  than one '*' refused (`:129-135`). get_cors_method_flags: exact strcmp GET/POST/PUT/DELETE/HEAD,
  never COPY (`:137-147`).
- Matching `src/rgw/rgw_cors.cc`: is_string_in_set `:109-150` ("*" anywhere in the set, exact
  member, else for each member with '*': split on "* \t" by get_str_list, prefix part must
  start h, suffix part must end h); has_wildcard_origin `:152-157`; is_origin_present
  `:159-162` (case-sensitive); is_header_allowed `:164-173` (lowercases both sides);
  format_exp_headers `:175-184` (joins with ",", replaces "\n" with "\\n");
  host_name_rule `:186-194` (first rule, in stored order, whose origins match).
- XML `src/rgw/rgw_cors_s3.cc`: RGWCORSRule_S3::to_xml `:32-74` (ID if non-empty;
  AllowedMethod in the fixed order GET, PUT, DELETE, HEAD, POST, COPY; AllowedOrigin sorted
  (set); AllowedHeader sorted; MaxAgeSeconds when valid; ExposeHeader in order);
  RGWCORSRule_S3::xml_end `:76-164` (AllowedMethod strcasecmp GET/POST/DELETE/HEAD/PUT/COPY,
  anything else fails; a rule with no AllowedMethod is accepted with flags 0; ID longer than
  255 fails; at least one AllowedOrigin, each passing validate_name_string; MaxAgeSeconds
  strtoull with trailing junk failing and >= 2^32 meaning invalid; AllowedHeader each passing
  validate_name_string); RGWCORSConfiguration_S3::to_xml `:166-175` (CORSConfiguration in
  XMLNS_AWS_S3); xml_end `:177-188` (at least one CORSRule); alloc_obj `:226-245`.

## CORS signing (verified 2026-10-13)

- get_v4_canonical_method `src/rgw/rgw_auth_s3.cc:705-732@v19.2.6`; called from
  get_auth_data_v4 after credentials, canonical headers, payload hash, URI and query
  (`src/rgw/rgw_rest_s3.cc:5827@v19.2.6`). At v20.2.4 get_canonical_method
  `src/rgw/rgw_auth_s3.cc:1749-1776@v20.2.4`, also used by the SigV2 canonical header at
  `src/rgw/rgw_auth_s3.cc:258@v20.2.4` (so SigV2 OPTIONS gets the Access-Control-Request-Method treatment only on
  Tentacle). For op type RGW_OP_OPTIONS_CORS: missing ACRM, or one get_cors_method_flags
  does not know -> throw -EINVAL (400 InvalidArgument).
- LocalEngine::authenticate skips the signature compare for RGW_OP_OPTIONS_CORS once the
  key's user loads, the account and policies load, and the key is in the user's record
  (`src/rgw/rgw_rest_s3.cc:6320-6360@v19.2.6`, `src/rgw/rgw_rest_s3.cc:6890-6931@v20.2.4`).
- rgw-go: auth verifies an OPTIONS over the method OPTIONS (internal/auth/sigv4.go:404-406,
  internal/auth/sigv2.go:98-100 comments @0239625); an unsigned OPTIONS is anonymous
  (internal/auth/verifier.go:62 @0239625).

## CORS tests at the pin (read 2026-10-13)

- test_set_cors `test_s3.py:7125-7151`; test_cors_origin_response `:7160-7233`;
  test_cors_origin_wildcard `:7235-7258`; test_cors_header_option `:7260-7284`;
  _test_cors_options_presigned_method `:7286-7323` (presigned OPTIONS without ACRM expects
  400; with CORS and ACRM expects 200 + ACAO + ACAM); test_cors_presigned_* `:7325-7398`, the
  four _v2 ones fails_on_rgw; _test_object_raw_get_x_amz_expires_not_expired (OPTIONS on a
  presigned GET URL expects 400, then GET 200); test_cors_presigned_url_non_preflight
  `:22104-...`.
- test_cors_origin_response expects 403 for a plain GET with a non-matching Origin
  (`:7187`). No radosgw code refuses by Origin at v19.2.6: HTTP_ORIGIN is read only at
  `src/rgw/rgw_op.cc:1544` (generate_cors_headers) and `:6180` (RGWOptionsCORS::execute).
  Code reading predicts radosgw answers 200 there and fails the test; UNRECORDED until the
  ledger records it.

## Static website (verified 2026-10-13)

- Ops identical at both tags except the forward argument: RGWGetBucketWebsite::verify_permission
  `src/rgw/rgw_op.cc:2862-2873@v19.2.6` / `src/rgw/rgw_op.cc:3094-3105@v20.2.4#RGWGetBucketWebsite::verify_permission` (s3:GetBucketWebsite);
  RGWGetBucketWebsite::execute `:2880-2885` / `:3112-3117` (!has_website ->
  -ERR_NO_SUCH_WEBSITE_CONFIGURATION); RGWSetBucketWebsite::verify_permission `:2887-2898` /
  `:3119-3130` (s3:PutBucketWebsite); RGWSetBucketWebsite::execute `:2905-2936` / `:3137-3168`
  (get_params, bucket_exists, forward, retry_raced_bucket_write setting has_website=true and
  website_conf, put_info: the configuration is a FIELD of RGWBucketInfo, not an attr);
  RGWDeleteBucketWebsite::verify_permission `:2938-2949` / `:3170-3181` (s3:DeleteBucketWebsite);
  RGWDeleteBucketWebsite::execute `:2956-2981` / `:3188-3213` (has_website=false, empty conf,
  put_info; succeeds with none set). pre_exec is rgw_bucket_object_pre_exec for all three.
- S3 REST identical at both tags: RGWSetBucketWebsite_ObjStore_S3::get_params
  `src/rgw/rgw_rest_s3.cc:2271-2337@v19.2.6` / `src/rgw/rgw_rest_s3.cc:2374-2440@v20.2.4#RGWSetBucketWebsite_ObjStore_S3::get_params` (read_all_input; parse
  failure -EINVAL (400 InvalidArgument, NOT MalformedXML); decode_xml("WebsiteConfiguration",
  mandatory) failure -EINVAL; redirect-all without HostName -> -EINVAL "A host name must be
  provided to redirect all requests (e.g. \"example.com\")."; neither redirect-all nor
  IndexDocument -> -EINVAL "A value for IndexDocument Suffix must be provided if
  RedirectAllRequestsTo is empty"; IndexDocument with empty Suffix -> -EINVAL "The
  IndexDocument Suffix is not well formed"; routing rules > rgw_website_routing_rules_max_num
  (negative -> 50) -> ERR_INVALID_WEBSITE_ROUTING_RULES_ERROR 400 InvalidRequest "<n> routing
  rules provided, the number of routing rules in a website configuration is limited to
  <max>."); Set send_response `:2339-2345` / `:2442-2448` (op passed); Delete send_response
  `:2347-2355` / `:2450-2458` (0 -> 204, op passed); Get send_response `:2357-2375` /
  `:2460-2478` (WebsiteConfiguration in XMLNS_AWS_S3 then conf.dump_xml; op passed).
- rgw_website.h and rgw_website.cc identical between tags. RGWBucketWebsiteConf
  `src/rgw/rgw_website.h:176-243@v19.2.6`: ENCODE_START(2,1){index_doc_suffix, error_doc,
  routing_rules, redirect_all, subdir_marker, listing_css_doc, listing_enabled}, decode reads
  the last three only for struct_v >= 2 (`:194-217`); is_redirect_all and is_set_index_doc
  are NOT encoded. RGWRedirectInfo ENCODE_START(1,1){protocol, hostname, http_redirect_code
  u16} (`:26-50`); RGWBWRedirectInfo (1,1){redirect, replace_key_prefix_with,
  replace_key_with} (`:53-79`); RGWBWRoutingRuleCondition (1,1){key_prefix_equals,
  http_error_code_returned_equals u16} (`:81-109`); RGWBWRoutingRule (1,1){condition,
  redirect_info} (`:111-147`); RGWBWRoutingRules (1,1){rules list} (`:149-174`).
- rgw_website.cc@v19.2.6: check_key_condition `:34-37` (prefix); apply_rule `:40-63`
  (protocol default, hostname default, "proto://host/" + replace prefix + rest of key, or
  replace key, or key; code only when > 0); check_key_and_error_code_condition `:65-74`
  (first rule matching both; an error code of 0 matches a rule with no error condition);
  should_redirect `:98-114` (redirect_all when hostname set; the rule copy keeps code 0,
  then the conf's member is set to 301 after the copy); get_effective_key `:116-133` (no
  suffix -> false; "" -> suffix; trailing "/" -> key+suffix; not a file -> key+"/"+suffix;
  file -> key); JSON dump `:196-205` writes redirect_all alone when its hostname is set,
  else index_doc_suffix, error_doc, routing_rules (subdir_marker, listing_css_doc,
  listing_enabled never dumped); decode_json `:207-212`; XML: RGWBWRedirectInfo::decode_xml
  `:235-251` (HttpRedirectCode must be 301..399 "The provided HTTP redirect code is not
  valid. Valid codes are 3XX except 300."; ReplaceKeyPrefixWith and ReplaceKeyWith together
  refused); RGWBWRoutingRuleCondition::decode_xml `:265-275` (400..599); dump_xml `:214-231`,
  `:253-261`, `:277-281`, `:293-316` (RedirectAllRequestsTo HostName then Protocol;
  IndexDocument Suffix; ErrorDocument Key; RoutingRules/RoutingRule); decode_xml `:323-341`
  (RedirectAllRequestsTo wins and the rest is ignored; HostName mandatory there).
- Options: rgw_enable_static_website default false (`src/common/options/rgw.yaml.in:2430-2437@v19.2.6`);
  rgw_website_routing_rules_max_num 50 (`:46-50`); rgw_cors_rules_max_num 100 (`:26-30`).
  The bucket op_get/op_put/op_delete return NULL for ?website when the option is off
  (`src/rgw/rgw_rest_s3.cc:4644-4649`, `:4703`, `src/rgw/rgw_rest_s3.cc:4767@v19.2.6`).
- Website mode: RGWRESTMgr_S3::get_handler picks the S3Website handlers when enable_s3website
  && prot_flags has RGW_REST_WEBSITE (`src/rgw/rgw_rest_s3.cc:5255-5277@v19.2.6`); format HTML.
  enable_s3website = rgw_enable_apis names s3website (`src/rgw/rgw_appmain.cc:301-312@v19.2.6`).
  RGWREST::preprocess (`src/rgw/rgw_rest.cc:2028-2179@v19.2.6`, `src/rgw/rgw_rest.cc:2045-2201@v20.2.4#RGWREST::preprocess`): api
  priority by reverse position in rgw_enable_apis; s3website hostnames =
  rgw_dns_s3website_name + zonegroup hostnames_s3website (`src/rgw/rgw_rest.cc:235-238@v19.2.6`); a Host in the
  s3website domains sets RGW_REST_WEBSITE; s3website listed before s3 sets it for every
  request with a Host; v20.2.4 ADDS an else-branch: with no Host header and s3website ahead of
  s3, RGW_REST_WEBSITE is set too (diff of the block).
- Website handler (`src/rgw/rgw_rest_s3website.h:20-100@v19.2.6`): only GET and HEAD
  (op_put/op_delete/op_post/op_copy/op_options NULL -> 405); every scope maps to
  RGWGetObj_ObjStore_S3Website. Handler code `src/rgw/rgw_rest_s3.cc:5316-5590@v19.2.6`
  (`src/rgw/rgw_rest_s3.cc:5876-6145@v20.2.4`), identical but web_dir's state load: web_dir `:5316-5340` (stat of the
  key less one trailing "/"); init `:5342-5355` (keeps original_object_name); retarget
  `:5357-5418` (no bucket -> NoSuchBucket; !has_website -> NoSuchWebsiteConfiguration;
  get_effective_key false -> -EINVAL "The IndexDocument Suffix is not configurated or not
  well formed!"; should_redirect(new key, 0) -> apply_rule with HTTP_HOST and http/https ->
  -ERR_WEBSITE_REDIRECT with s->err.http_ret = custom code; else s->object = effective key);
  serve_errordoc `:5430-5501` (a nested GET of the error doc with conditionals cleared and
  the original status as custom_http_ret; any failure -> -1, "double error handler");
  error_handler `:5503-5555` (redirect by original key + mapped HTTP code; else error doc).
  retarget runs in rgw_process_authenticated after init_permissions and before
  read_permissions (`src/rgw/rgw_process.cc:180-190@v19.2.6`).
- RGWGetObj_ObjStore_S3Website::send_response_data `src/rgw/rgw_rest_s3.cc:266-284@v19.2.6`
  (`src/rgw/rgw_rest_s3.cc:271-289@v20.2.4#RGWGetObj_ObjStore_S3Website::send_response_data`): object attr user.rgw.x-amz-website-redirect-location -> 301 with
  Location, Content-Length 0.
- Error document in website mode is HTML (HTMLFormatter, `src/rgw/rgw_rest.cc:1803@v19.2.6`;
  `src/common/HTMLFormatter.cc@v19.2.6` output_header: <html><head><title>STATUS
  NAME</title></head><body><h1>STATUS NAME</h1><ul>, entries <li>Name: value</li>); dump()
  skips the Error section for HTML (`src/rgw/rgw_common.cc` dump(req_state*)).
- s3-tests at the pin has NO website test: pytest.ini declares s3website,
  s3website_routing_rules, s3website_redirect_location (`pytest.ini:43-45`) but no test in
  s3tests/ carries them and no file mentions "website".
- rgw-go: meta.BucketInfo.Website is an opaque RawStruct (internal/meta/bucket_info.go:101-102,
  :173-176, :262-264 @0239625; websiteVersion 2 at :72); JSON dump/put refuse a bucket with a
  website (bucket_info.go:351-356, bucket_json.go:125-126, dump_bucket.go:50-51 @0239625).
  cephconf.EnabledAPIs treats s3website as enabling S3 and lists it as ignored
  (internal/cephconf/apis.go:18-41 @0239625). internal/vhost knows no s3website hostnames.
  op.BucketStore.PutBucketInfo exists (internal/op/bucket.go:114-115 @0239625).

## Public access block and policy status (verified 2026-10-13)

- RGWGetBucketPolicyStatus::verify_permission `src/rgw/rgw_op.cc:8582-8593@v19.2.6` /
  `src/rgw/rgw_op.cc:9563-9574@v20.2.4#RGWGetBucketPolicyStatus::verify_permission` (s3:GetBucketPolicyStatus); execute `:8595-8598` / `:9576-9579`:
  isPublic = (s->iam_policy && rgw::IAM::is_public(*s->iam_policy)) ||
  s->bucket_acl.is_public(this). Ignores the block.
- RGWPutBucketPublicAccessBlock::verify_permission `:8600-8611` / `:9581-9592`
  (s3:PutBucketPublicAccessBlock); get_params `:8613-8618` / `:9594-9599`; execute
  `:8620-8662` / `:9601-9643` (parser init -EINVAL; parse failure MalformedXML;
  decode_xml("PublicAccessBlockConfiguration", mandatory) failure MalformedXML; forward;
  retry_raced_bucket_write merging s->bucket_attrs, the attrs the request STARTED with, plus
  user.rgw.public-access: the same stale write-back as PutBucketPolicy's).
- RGWGetBucketPublicAccessBlock::verify_permission `:8664-8675` / `:9645-9656`; execute
  `:8677-8699` / `:9658-9680` (no attr -> ERR_NO_SUCH_PUBLIC_ACCESS_BLOCK_CONFIGURATION with
  message "The public access block configuration was not found"; decode failure -EIO).
- RGWDeleteBucketPublicAccessBlock::send_response `:8702-8712` / `:9683-9693` (0 -> 204, no op);
  verify_permission `:8714-8725` / `:9695-9706` (s3:PutBucketPublicAccessBlock); execute
  `:8727-8742` / `:9708-9723` (erase from s->bucket->get_attrs(), put_info).
- S3 REST identical: RGWGetBucketPolicyStatus_ObjStore_S3::send_response
  `src/rgw/rgw_rest_s3.cc:4554-4572@v19.2.6` / `src/rgw/rgw_rest_s3.cc:5095-5113@v20.2.4#RGWGetBucketPolicyStatus_ObjStore_S3::send_response` (PolicyStatus in
  XMLNS_AWS_S3, IsPublic as lower-case true/false); Put PAB send_response `:4574-4581` /
  `:5115-5122` (200, no op); Get PAB send_response `:4583-4594` / `:5124-5135`.
- Get PAB send_response writes access_conf.dump_xml after end_header WITHOUT checking op_ret:
  on a bucket without a block, end_header has already sent the error document and its
  Content-Length (`src/rgw/rgw_rest.cc:620-625@v19.2.6`), then dump_xml + 
  rgw_flush_formatter_and_reset (`:303-314`) send a default PublicAccessBlockConfiguration
  after it, past the Content-Length; no IO filter truncates (ConLenControllingFilter only
  inhibits a length on 204/304, `src/rgw/rgw_client_io_filters.h:327-365@v19.2.6`).
  CANDIDATE DEFECT, unreproduced: by source reading only.
- PublicAccessBlockConfiguration XML (`src/rgw/rgw_public_access.cc:4-20@v19.2.6`, file
  identical at v20.2.4): decode_xml reads the four booleans, each optional; dump_xml writes
  PublicAccessBlockConfiguration with NO namespace, each boolean as true/false.
- Error map (`src/rgw/rgw_common.cc@v19.2.6`): ERR_WEBSITE_REDIRECT 301 WebsiteRedirect
  (:58); EINVAL 400 InvalidArgument (:61); ERR_INVALID_REQUEST 400 (:62); ERR_MALFORMED_XML
  (:77); ERR_INVALID_CORS_RULES_ERROR 400 InvalidRequest (:82);
  ERR_INVALID_WEBSITE_ROUTING_RULES_ERROR 400 InvalidRequest (:83); ERR_NO_SUCH_WEBSITE_CONFIGURATION
  404 (:99); ERR_NO_CORS_FOUND 404 NoSuchCORSConfiguration (:106);
  ERR_NO_SUCH_BUCKET_ENCRYPTION_CONFIGURATION 404 ServerSideEncryptionConfigurationNotFoundError
  (:138); ERR_NO_SUCH_PUBLIC_ACCESS_BLOCK_CONFIGURATION 404 (:139). Same rows at v20.2.4 one
  line lower (:59-140). rgw-go already defines each (internal/op/errors.go:62-119 @0239625).
- Phase 1 already honors a stored block: authz.publicAccess (internal/authz/load.go:70-92),
  requestBucket/explicitBucket/explicitPrelude (internal/authz/evaluator.go:249-345),
  restricted (RestrictPublicBuckets, v20.2.4 semantics on both, :581-598), ignorePublicACLs
  (:600-603); write refusals authz/write.go:112-159 (BlockPublicAcls) and :255-267
  (BlockPublicPolicy); op.blockPublicACLs (internal/op/putobject.go:198-211),
  op.blocksPublicPolicy (internal/op/bucketsubres.go:352-363) @0239625.

## RULE CHANGE from the owner (received mid-research, 2026-10-13; replaces the brief's PP-3 bullet)

Compatibility means RADOS state in both directions, not bug-for-bug S3 behaviour. rgw-go serves
whatever RADOS state radosgw wrote, and radosgw serves whatever rgw-go writes. Where radosgw's
S3 behaviour is wrong, rgw-go does the correct thing for the S3 API (usually AWS S3's documented
behaviour) as long as the stored RADOS form (the xattr or struct) stays the one radosgw writes
and reads. rgw-go prevents corruption of shared data even where radosgw allows it. Each
difference is an exclusion; a serious defect is reported upstream once a running release
reproduces it. Every defect gets: class (security, data corruption, S3-correctness, cosmetic),
reproduction status, rgw-go's choice.

## POST object (verified 2026-10-13)

- RGWPostObj `src/rgw/rgw_op.h:1325-1375@v19.2.6`: name post_obj, RGW_OP_POST_OBJ,
  RGW_OP_TYPE_WRITE, min_len 0 / max_len LLONG_MAX.
- RGWPostObj::init_processing `src/rgw/rgw_op.cc:4576-4590@v19.2.6` / `src/rgw/rgw_op.cc:4869-4883@v20.2.4#RGWPostObj::init_processing`
  (get_params, then verify_params, then RGWOp::init_processing): the form is read and its
  credentials authenticated in init_processing, AFTER verify_requester, postauth_init, the
  suspended-user check (`src/rgw/rgw_process.cc:372-376@v19.2.6`, `src/rgw/rgw_process.cc:374-378@v20.2.4`) and
  init_permissions, and BEFORE verify_op_mask and verify_permission.
- RGWPostObj::verify_permission `:4592-4605` / `:4885-4898` (rgw_iam_add_crypt_attrs, then
  verify_bucket_permission(ARN(object), s3:PutObject)). No BlockPublicAcls check anywhere in
  RGWPostObj (contrast PutObj's canned-ACL check `src/rgw/rgw_op.cc:3903-3909@v19.2.6`).
- RGWPostObj::execute `src/rgw/rgw_op.cc:4612-4796@v19.2.6#RGWPostObj::execute` / `src/rgw/rgw_op.cc:4905-5139@v20.2.4#RGWPostObj::execute`: notification reserve;
  per file: quota check with content_length; Content-MD5 decode (bad -> InvalidDigest); object
  from the form key; versioned bucket -> random instance; atomic writer; encrypt filter or
  compression; stream with ofs > max_len -> ERR_TOO_LARGE (400 EntityTooLarge); ofs < min_len
  -> ERR_TOO_SMALL (400 EntityTooSmall); quota check with obj_size; MD5 mismatch BadDigest;
  attrs etag, acl, content type (NUL-terminated), compression; complete. v20.2.4 adds the
  streaming checksum filter from the request env and RGW_ATTR_CKSUM, mismatch BadDigest.
- Form parser, identical at both tags (`src/rgw/rgw_rest.cc:1107-1471@v19.2.6`,
  `src/rgw/rgw_rest.cc:1112-1476@v20.2.4`): parse_boundary_params `:1107-1140`; parse_part_field `:1142-1155`;
  index_of / read_with_boundary `:1166-1287`; read_form_part_header `:1306-1365` (lines
  trimmed; Content-Disposition's name param names the part; lines up to rgw_max_chunk_size);
  part_str trims whitespace `:1367-1380`; verify_params `:1408-1424` (no CONTENT_LENGTH ->
  ERR_LENGTH_REQUIRED 411 MissingContentLength; > rgw_max_put_size -> EntityTooLarge;
  supplied MD5 from HTTP_CONTENT_MD5); get_params `:1426-1469` (100-continue sent; Content-Type
  must be exactly multipart/form-data -> else -EINVAL "Request Content-Type is not
  multipart/form-data"; boundary param required "Missing multipart boundary specification").
  parts is a map keyed case-insensitively (ltstr_nocase, `src/rgw/rgw_rest.h:247-249@v19.2.6`).
- RGWPostObj_ObjStore_S3 (`src/rgw/rgw_rest_s3.h:288-321@v19.2.6`): rebuild_key
  `src/rgw/rgw_rest_s3.cc:2881-2894@v19.2.6` (first "${filename}" replaced);
  get_params `src/rgw/rgw_rest_s3.cc:2906-3070@v19.2.6#RGWPostObj_ObjStore_S3::get_params` / `src/rgw/rgw_rest_s3.cc:3040-3214@v20.2.4#RGWPostObj_ObjStore_S3::get_params` (map_qs_metadata; parts until the
  "file" part, whose Content-Disposition filename is kept; a "done" before file -> -EINVAL
  "Malformed request"; each non-file part added to the policy env with its RAW data;
  x-amz-server-side-encryption* parts into crypt_attribute_map; v20.2.4 also maps
  x-amz-checksum-* parts into HTTP_ env props; get_encryption_defaults; env bucket; key part
  required "Key not specified"; rebuild_key; empty key "Empty object name"; env key;
  Content-Type part; x-amz-storage-class must be a valid placement "The storage class you
  specified is not valid"; x-amz-meta-* parts and x-amz-website-redirect-location part as
  NUL-terminated attrs; get_policy; get_tags; min/max from the policy). get_tags `:3072-3110`
  (tagging part XML, -EINVAL on bad XML). get_policy `:3112-3246` (policy part present:
  x-amz-algorithm == AWS4-HMAC-SHA256 selects v4 needing x-amz-credential, x-amz-signature,
  x-amz-date ("Missing aws4 credential/signature/date"); else v2 needing AWSAccessKeyId and
  signature ("Missing aws2 access key/signature"); empty x-amz-security-token "Invalid token";
  Strategy::apply(get_s3_post) failure -> -EACCES; policy base64 decode "Could not decode
  policy"; from_json; v4 marks x-amz-signature checked, v2 AWSAccessKeyId and signature;
  policy always; check. No policy part: "No attached policy found!", identity unchanged
  (anonymous). Then acl part through create_canned_acl, failure "Bad canned ACLs").
  complete_get_params `:3248-3270` (drains parts after the file, storing nothing); get_data
  `:3272-3295`; send_response `:3297-3412` (success_action_redirect: redirect + ?bucket= (or
  ?tenant=&bucket=) &key=&etag= url-encoded, check_utf8, 303 STATUS_REDIRECT;
  success_action_status 200 -> 200, 201 -> 201 with PostResponse{Location (compute_domain_uri
  /bucket/key, or base/tenant:bucket/key plus Tenant), Bucket, Key, ETag}, anything else ->
  204; default 204 with ETag header; err_msg becomes the message). All identical at v20.2.4
  except get_params' checksum mapping.
- Browser-upload abstractor `src/rgw/rgw_rest_s3.cc:6117-6171@v19.2.6`: v2 signs the encoded
  policy with get_v2_signature; v4 takes the access key from x-amz-credential up to the first
  "/", the rest as the scope, and signs the encoded policy with get_v4_signature over that
  scope; no date or region check; AWSEngine::authenticate denies -EINVAL for an empty key or
  signature. Strategy::apply `src/rgw/rgw_auth.cc:489-555@v19.2.6` replaces s->auth.identity,
  loads the user (load_acct_info) and the identity policies (modify_request_state).
- POST policy, rgw_policy_s3.{h,cc}: identical but v20.2.4's match_policy_vars also skips
  checksum header names (`src/rgw/rgw_policy_s3.cc:108-130@v20.2.4`). v19.2.6
  (`src/rgw/rgw_policy_s3.cc`): eq (exact) `:53-62`; starts-with `:64-73`; env vars case-
  insensitive `:75-102` ("$name" reads a var and marks it checked); match_policy_vars
  `:105-121` (every env var not x-ignore-* must be checked, else "Policy missing condition:
  <name>"); set_expires parse_iso8601 `:132-141`; add_condition `:143-187` (eq, starts-with,
  content-length-range tightening min/max via stringtoll, "Bad content-length-range param";
  other op "Invalid condition: <op>"); check `:189-235` (expires <= now -> -EACCES "Policy
  expired"; simple checks var missing or different -> -EACCES; conditions; missing
  conditions -> -EACCES); from_json `:238-305` ("Malformed JSON"; "Policy missing
  expiration"; "Failed to parse policy expiration"; "Policy missing conditions"; array
  conditions need exactly 3 elements "Bad condition array, expecting 3 arguments"; an object
  condition checks its FIRST member only; anything else -EINVAL).
- Status codes `src/rgw/rgw_common.cc@v19.2.6`: STATUS_CREATED 201 (:53), STATUS_NO_CONTENT
  204 (:55), STATUS_REDIRECT 303 (:59), ERR_TOO_SMALL 400 EntityTooSmall (:75),
  ERR_LENGTH_REQUIRED 411 MissingContentLength (:87).
- s3-tests at the pin: all 35 deselected post_object tests use SigV2 form fields; none uses
  x-amz-algorithm. Other POST tests carry excluded markers: SSE-C/KMS/S3 POST tests
  (encryption, bucket_encryption, sse_s3) and test_post_object_upload_checksum (checksum,
  test_s3.py:16362-16416: checksum field accepted 204, bad checksum 400).

## Candidate defects found (all UNREPRODUCED, source reading only)

- POST object skips the suspended-user check: the only check is in process_request before the
  form's credentials replace the anonymous identity (`src/rgw/rgw_process.cc:372-376@v19.2.6`,
  `src/rgw/rgw_process.cc:374-378@v20.2.4`; get_policy's Strategy::apply `src/rgw/rgw_rest_s3.cc:3182@v19.2.6`, `src/rgw/rgw_rest_s3.cc:3326@v20.2.4#RGWPostObj_ObjStore_S3::get_policy`).
  Class: security (fail-open: a suspended user can upload). Prior-art search not done.
- POST object ignores BlockPublicAcls for a public canned acl form field (no check in
  RGWPostObj or get_policy). Class: security (an owner's block bypassed). Extends the
  registry's "radosgw lets a public ACL through a block of public ACLs on PutObject's grant
  headers, CopyObject and CreateMultipartUpload".
- GetBucketPublicAccessBlock on a bucket without a block writes a default
  PublicAccessBlockConfiguration after the error document, past Content-Length. Class:
  S3-correctness (HTTP framing; can desynchronize a keep-alive connection).
- PutBucketCors and PutPublicAccessBlock retries merge the attrs the request started with
  (`rgw_op.cc:6100-6104`, `src/rgw/rgw_op.cc:8656-8660@v19.2.6`): same class as the registry's PutBucketPolicy
  entry (data corruption: a lost update of another bucket attr).
- Ownership-controls requests on a floor radosgw fall through to ListObjects / CreateBucket /
  DeleteBucket; DELETE ?ownershipControls on an empty bucket deletes the bucket. Class:
  S3-correctness with data loss of the bucket (not of objects). Intended-by-absence; upstream
  fixed by implementing the feature in v21.

## Upstream main after the floors (verified 2026-10-13 against origin/main 292995374d9)

- The local ceph `main` ref is stale (7ed73efc1be); origin/main is 292995374d9 (2026-10-12).
- 4e056f98e78 (2026-07-27, "RGW: Verify non-preflight authenticated cross origin requests"),
  then 8d652cd24c1 and 7f147f02058 (2026-08-17): RGWOp::verify_cors_match
  (`src/rgw/rgw_op.cc:2088-2133@292995374d9`) refuses with -EACCES a non-OPTIONS request with a
  non-empty Origin on a bucket (or global) CORS config when no rule matches the origin and
  the request's own method; no config -> 0. Called after verify_op_mask, before OPA and
  verify_permission (`src/rgw/rgw_process.cc:290-299@292995374d9`). No tag carries it.
  s3-tests' test_cors_presigned_url_non_preflight came with 4371cb70167 (2026-08-05) for it.
- AWS documents the same refusal: "For each request that Amazon S3 receives, you must have a
  CORS rule in your CORS configuration that matches the data in your request"; "403 Forbidden
  ... This CORS request is not allowed" for origin, method or requested headers not allowed
  (https://docs.aws.amazon.com/AmazonS3/latest/userguide/cors-troubleshooting.html, fetched
  2026-10-13); evaluation uses "the first CORSRule rule that matches the incoming browser
  request" on origin, Access-Control-Request-Method and Access-Control-Request-Headers
  (https://docs.aws.amazon.com/AmazonS3/latest/userguide/cors.html).
- cc1dcecee08 (2026-05-31, "rgw: match CORS rules like Amazon S3", PR #69190): rule selection
  becomes the first rule matching origin AND method AND (preflight) headers
  (RGWCORSRule::matches, RGWCORSConfiguration::match_rule in `src/rgw/rgw_cors.cc@cc1dcecee08`);
  the floors take the first rule matching the origin alone and then test its method
  (host_name_rule, `src/rgw/rgw_cors.cc:186-194@v19.2.6`). The stored form is unchanged on main
  (`src/rgw/rgw_cors.h` ENCODE_START(1,1) at :69 and :115 @292995374d9).
- 3d6f0fe0f81 and later: "global CORS" (a new configuration source) on main only; not a fix.
- 7d2484c45b4 (2026-04-23, tracker #76157): POST object bytes counted in the usage log
  (ACCOUNTING_IO set_account around read_with_boundary's recv_body); the floors log
  bytes_received 0 for POST.
- 96ed8b8c780 (2026-08-26): BlockedEncryptionTypes in bucket encryption config, main only.
- Ownership controls: first in v21.0.0 (b3733296c8e). v21.1.2's stored form:
  `src/rgw/rgw_object_ownership.h@v21.1.2` enum ObjectOwnership u8 {BucketOwnerEnforced=0,
  BucketOwnerPreferred=1, ObjectWriter=2}; encode ENCODE_START(1,1){object_ownership}
  (`src/rgw/rgw_object_ownership.cc:77-89@v21.1.2`); a missing or undecodable attr means
  ObjectWriter (`:91-106`). Ops RGWPut/Get/DeleteBucketOwnershipControls
  `src/rgw/rgw_op.cc:10247-10360@v21.1.2`; PutACLs refuses ACLs under BucketOwnerEnforced
  "Cannot set ACLs when ObjectOwnership is BucketOwnerEnforced." (`src/rgw/rgw_op.cc:6868-6869@v21.1.2`).
  About 50 references across rgw_op.cc and rgw_rest_s3.cc at v21.1.2.
- PublicAccessBlockConfiguration encoding unchanged on main (ENCODE_START(1,1), four bools,
  `src/rgw/rgw_public_access.h:25-45@292995374d9`); main adds account-level blocks (S3
  Control, 8924fe65f8f) and BlockPublicAcls on CreateBucket (8566ec987c7), both with accounts'
  S3 Control (phase 3).
- AWS ServerSideEncryptionByDefault: SSEAlgorithm valid values "AES256 | aws:fsx |
  aws:backup | aws:kms | aws:kms:dsse"; KMSMasterKeyID "allowed if and only if SSEAlgorithm is
  set to aws:kms or aws:kms:dsse"
  (https://docs.aws.amazon.com/AmazonS3/latest/API/API_ServerSideEncryptionByDefault.html,
  fetched 2026-10-13). The floors validate neither.
- Registry (docs/ceph-upstream-bugs.md @0239625): "Squid's is_public counts every Allow
  statement whose NotPrincipal is not the wildcard" (rgw-go follows the zone's release) and
  "radosgw's is_public judges a wildcard-principal statement against a fixed three-key
  environment" (security; rgw-go reproduces) both name GetBucketPolicyStatus's IsPublic as a
  consumer.
- go-ceph rgw/admin suite: hack/admin/deselect-phase2.txt @0239625 lists only versioning and
  object-lock subtests; none in this area.
- Union of the encryption, sse_s3 and bucket_encryption markers in test_s3.py at the pin: 71 tests (markers.py, 2026-10-13).
