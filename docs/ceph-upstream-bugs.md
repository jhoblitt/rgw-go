# Upstream Ceph defects

This registry records the defects in upstream ceph/ceph that rgw-go runs
into: in radosgw, the RGW object classes, librados, librbd, the monitors, or
the OSD behaviour they rely on. rgw-go must coexist with radosgw on the same
pools, so a defect in radosgw's behaviour is part of the compatibility
contract (section 8 of the design spec). The registry says, per defect:
- which releases carry it;
- whether rgw-go reproduces it, works around it, or is unaffected;
- where it stands upstream.

Each entry names its kind:

- **Defect** means upstream code does the wrong thing, whether or not
  upstream has fixed it in a later release.
- **Quirk** means the behaviour is intended upstream but surprises a
  reimplementation, for example a round trip that is not byte-identical.

Each entry records its evidence at a named release tag, how it was found,
and, under **Upstream**, its tracker issues and pull requests.
Every claim is verified against the source or a cluster, not taken from a
report. Add an entry whenever a task, review or benchmark hits an upstream
defect or quirk. Update it when an upstream issue is filed, when upstream
fixes it, or when rgw-go's handling changes. go-ceph's defects live in
`docs/cgo-limitations.md`, not here.

| Entry | Upstream issues | Upstream fix PRs | Found by us |
| --- | --- | --- | --- |
| [Squid does not guard listing-time index suggestions against resharding](#squid-does-not-guard-listing-time-index-suggestions-against-resharding) | [#81000](https://tracker.ceph.com/issues/81000), [#81217](https://tracker.ceph.com/issues/81217) | [ceph/ceph#59609](https://github.com/ceph/ceph/pull/59609), [ceph/ceph#72254](https://github.com/ceph/ceph/pull/72254) |  |
| [check_disk_state removes a multipart part's index entry from the wrong shard](#check_disk_state-removes-a-multipart-parts-index-entry-from-the-wrong-shard) | [#81121](https://tracker.ceph.com/issues/81121) | [ceph/ceph#72306](https://github.com/ceph/ceph/pull/72306) | ✓ |
| [cls_rgw complete_op writes a stale epoch back when it cancels](#cls_rgw-complete_op-writes-a-stale-epoch-back-when-it-cancels) | [#80894](https://tracker.ceph.com/issues/80894), [#81002](https://tracker.ceph.com/issues/81002) | [ceph/ceph#72097](https://github.com/ceph/ceph/pull/72097) |  |
| [cls_rgw encodes a packed value of exactly 65536 as 0](#cls_rgw-encodes-a-packed-value-of-exactly-65536-as-0) | [#80995](https://tracker.ceph.com/issues/80995) | [ceph/ceph#72307](https://github.com/ceph/ceph/pull/72307) | ✓ |
| [Squid before 19.2.3 refuses a delete marker on top of a delete marker](#squid-before-1923-refuses-a-delete-marker-on-top-of-a-delete-marker) | none | [ceph/ceph#54957](https://github.com/ceph/ceph/pull/54957), [ceph/ceph#62740](https://github.com/ceph/ceph/pull/62740) |  |
| [cls_rgw usage trim never removes a payer-keyed record](#cls_rgw-usage-trim-never-removes-a-payer-keyed-record) | [#72593](https://tracker.ceph.com/issues/72593), [#80999](https://tracker.ceph.com/issues/80999) | [ceph/ceph#65329](https://github.com/ceph/ceph/pull/65329) |  |
| [cls_rgw usage trim with a bucket filter stalls behind 1000 other records](#cls_rgw-usage-trim-with-a-bucket-filter-stalls-behind-1000-other-records) | [#58136](https://tracker.ceph.com/issues/58136) | [ceph/ceph#49168](https://github.com/ceph/ceph/pull/49168) |  |
| [radosgw faults on a zero rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards](#radosgw-faults-on-a-zero-rgw_gc_max_objs-rgw_lc_max_objs-or-rgw_usage_max_shards) | [#80991](https://tracker.ceph.com/issues/80991) | [ceph/ceph#72160](https://github.com/ceph/ceph/pull/72160) | ✓ |
| [radosgw truncates a long aws-chunked trailer section instead of rejecting it](#radosgw-truncates-a-long-aws-chunked-trailer-section-instead-of-rejecting-it) | [#81122](https://tracker.ceph.com/issues/81122) | [ceph/ceph#72308](https://github.com/ceph/ceph/pull/72308) | ✓ |
| [radosgw accepts a negative or overflowing aws-chunked chunk size](#radosgw-accepts-a-negative-or-overflowing-aws-chunked-chunk-size) | [#81123](https://tracker.ceph.com/issues/81123) | [ceph/ceph#72309](https://github.com/ceph/ceph/pull/72309) | ✓ |
| [radosgw writes ACL owner and grantee names into its XML unescaped](#radosgw-writes-acl-owner-and-grantee-names-into-its-xml-unescaped) | none | none | ✓ |
| [radosgw ignores a payload-hash mismatch on bodies read by read_all_input](#radosgw-ignores-a-payload-hash-mismatch-on-bodies-read-by-read_all_input) | [#81230](https://tracker.ceph.com/issues/81230) | [ceph/ceph#72263](https://github.com/ceph/ceph/pull/72263) | ✓ |
| [radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose Content-Type is unsigned](#radosgw-1926-and-2024-reject-a-sigv4-request-whose-content-type-is-unsigned) | [#79674](https://tracker.ceph.com/issues/79674), [#79708](https://tracker.ceph.com/issues/79708), [#79723](https://tracker.ceph.com/issues/79723), [#79725](https://tracker.ceph.com/issues/79725), [#79724](https://tracker.ceph.com/issues/79724) | [ceph/ceph#71192](https://github.com/ceph/ceph/pull/71192), [ceph/ceph#71296](https://github.com/ceph/ceph/pull/71296), [ceph/ceph#71364](https://github.com/ceph/ceph/pull/71364), [ceph/ceph#71363](https://github.com/ceph/ceph/pull/71363) |  |
| [radosgw's SigV4 signing key is undefined for a secret byte above 0x7f](#radosgws-sigv4-signing-key-is-undefined-for-a-secret-byte-above-0x7f) | none | none | ✓ |
| [radosgw checks a copy source with inputs from the destination bucket](#radosgw-checks-a-copy-source-with-inputs-from-the-destination-bucket) | [#81248](https://tracker.ceph.com/issues/81248) | [ceph/ceph#72270](https://github.com/ceph/ceph/pull/72270) | ✓ |
| [radosgw terminates on a copy source or system request whose bucket policy does not parse](#radosgw-terminates-on-a-copy-source-or-system-request-whose-bucket-policy-does-not-parse) | [#81253](https://tracker.ceph.com/issues/81253) | [ceph/ceph#72271](https://github.com/ceph/ceph/pull/72271) | ✓ |
| [radosgw checks a CopyObject source against its bucket's ACL, not the object's](#radosgw-checks-a-copyobject-source-against-its-buckets-acl-not-the-objects) | none | none | ✓ |
| [Squid accepts RestrictPublicBuckets but never enforces it](#squid-accepts-restrictpublicbuckets-but-never-enforces-it) | [#65741](https://tracker.ceph.com/issues/65741), [#70860](https://tracker.ceph.com/issues/70860), [#70859](https://tracker.ceph.com/issues/70859) | [ceph/ceph#57206](https://github.com/ceph/ceph/pull/57206) |  |
| [radosgw answers 200 to a CreateBucket that loses a race to another owner](#radosgw-answers-200-to-a-createbucket-that-loses-a-race-to-another-owner) | [#76398](https://tracker.ceph.com/issues/76398) | [ceph/ceph#68722](https://github.com/ceph/ceph/pull/68722) |  |
| [radosgw's admin API bypass-gc removal leaks a tail and runs unbounded](#radosgws-admin-api-bypass-gc-removal-leaks-a-tail-and-runs-unbounded) | [#81304](https://tracker.ceph.com/issues/81304) | [ceph/ceph#72304](https://github.com/ceph/ceph/pull/72304) | ✓ |
| [radosgw's bypass-gc bucket removal fails once a tail stripe is gone](#radosgws-bypass-gc-bucket-removal-fails-once-a-tail-stripe-is-gone) | [#24789](https://tracker.ceph.com/issues/24789), [#40587](https://tracker.ceph.com/issues/40587) | [ceph/ceph#28789](https://github.com/ceph/ceph/pull/28789), [ceph/ceph#30198](https://github.com/ceph/ceph/pull/30198), [ceph/ceph#29984](https://github.com/ceph/ceph/pull/29984), [ceph/ceph#29956](https://github.com/ceph/ceph/pull/29956) |  |
| [The 2pc queue's reserved size drifts upward](#the-2pc-queues-reserved-size-drifts-upward) | none | none |  |
| [The 2pc queue's self-heal is skipped when another write comes first](#the-2pc-queues-self-heal-is-skipped-when-another-write-comes-first) | [#80994](https://tracker.ceph.com/issues/80994) | [ceph/ceph#72212](https://github.com/ceph/ceph/pull/72212) | ✓ |
| [The 2pc queue hands out reservation id 0, which radosgw treats as none](#the-2pc-queue-hands-out-reservation-id-0-which-radosgw-treats-as-none) | [#80996](https://tracker.ceph.com/issues/80996) | [ceph/ceph#72163](https://github.com/ceph/ceph/pull/72163) | ✓ |
| [radosgw's notification queue listing never pages past 1024 queues](#radosgws-notification-queue-listing-never-pages-past-1024-queues) | [#73812](https://tracker.ceph.com/issues/73812), [#73893](https://tracker.ceph.com/issues/73893), [#73894](https://tracker.ceph.com/issues/73894) | [ceph/ceph#66246](https://github.com/ceph/ceph/pull/66246), [ceph/ceph#66345](https://github.com/ceph/ceph/pull/66345), [ceph/ceph#66491](https://github.com/ceph/ceph/pull/66491) |  |
| [Squid's realm reload hangs when a pubsub HTTP push has lost its wakeup](#squids-realm-reload-hangs-when-a-pubsub-http-push-has-lost-its-wakeup) | none | [ceph/ceph#57632](https://github.com/ceph/ceph/pull/57632) |  |
| [radosgw's realm reload waits out the notification manager's timers](#radosgws-realm-reload-waits-out-the-notification-managers-timers) | [#71963](https://tracker.ceph.com/issues/71963), [#72004](https://tracker.ceph.com/issues/72004), [#72003](https://tracker.ceph.com/issues/72003) | [ceph/ceph#63986](https://github.com/ceph/ceph/pull/63986) |  |
| [radosgw caches any control-pool UPDATE_OBJ notify payload unchecked](#radosgw-caches-any-control-pool-update_obj-notify-payload-unchecked) | none | none |  |
| [radosgw aborts the process after 100 failed control-watch re-registrations](#radosgw-aborts-the-process-after-100-failed-control-watch-re-registrations) | [#80992](https://tracker.ceph.com/issues/80992) | [ceph/ceph#72251](https://github.com/ceph/ceph/pull/72251) |  |
| [radosgw abandons a control watch whose control object is deleted](#radosgw-abandons-a-control-watch-whose-control-object-is-deleted) | [#59217](https://tracker.ceph.com/issues/59217) | none |  |
| [radosgw keeps only the low 32 bits of rgw_num_control_oids](#radosgw-keeps-only-the-low-32-bits-of-rgw_num_control_oids) | none | none |  |
| [radosgw adds STANDARD to an empty placement target on decode](#radosgw-adds-standard-to-an-empty-placement-target-on-decode) | none | none |  |
| [radosgw lets a matching referer grant replace the requester's own ACL grants](#radosgw-lets-a-matching-referer-grant-replace-the-requesters-own-acl-grants) | [#18841](https://tracker.ceph.com/issues/18841) | [ceph/ceph#14344](https://github.com/ceph/ceph/pull/14344) |  |
| [radosgw ends a Referer's userinfo at an @ in its path](#radosgw-ends-a-referers-userinfo-at-an--in-its-path) | none | none | ✓ |
| [cls_version's header documents EAGAIN, but the class returns ECANCELED](#cls_versions-header-documents-eagain-but-the-class-returns-ecanceled) | none | none |  |
| [cls_lock get_info and assert_locked fail with EIO on an expired ephemeral lock](#cls_lock-get_info-and-assert_locked-fail-with-eio-on-an-expired-ephemeral-lock) | [#80993](https://tracker.ceph.com/issues/80993), [#56575](https://tracker.ceph.com/issues/56575) | none |  |
| [cls_otp divides by a stored step_size that is never validated](#cls_otp-divides-by-a-stored-step_size-that-is-never-validated) | [#80948](https://tracker.ceph.com/issues/80948) | [ceph/ceph#72250](https://github.com/ceph/ceph/pull/72250) | ✓ |
| [cls_otp computes the replay index from an unsigned window distance](#cls_otp-computes-the-replay-index-from-an-unsigned-window-distance) | [#80949](https://tracker.ceph.com/issues/80949) | [ceph/ceph#72253](https://github.com/ceph/ceph/pull/72253) | ✓ |
| [librbd leaks the update-watch context when registration fails](#librbd-leaks-the-update-watch-context-when-registration-fails) | none | none |  |
| [The monitor's default for insecure key creation lags auth_allowed_ciphers](#the-monitors-default-for-insecure-key-creation-lags-auth_allowed_ciphers) | [#80997](https://tracker.ceph.com/issues/80997) | none | ✓ |
| [The monitor reports a refused cephx key type as EINVAL](#the-monitor-reports-a-refused-cephx-key-type-as-einval) | [#80998](https://tracker.ceph.com/issues/80998) | none | ✓ |
| [The --name error lists the entity types as raw bytes](#the---name-error-lists-the-entity-types-as-raw-bytes) | [#79678](https://tracker.ceph.com/issues/79678), [#79640](https://tracker.ceph.com/issues/79640), [#79638](https://tracker.ceph.com/issues/79638) | [ceph/ceph#71165](https://github.com/ceph/ceph/pull/71165), [ceph/ceph#71190](https://github.com/ceph/ceph/pull/71190), [ceph/ceph#71191](https://github.com/ceph/ceph/pull/71191) |  |
| [radosgw ignores a bad port in an IPv6 endpoint](#radosgw-ignores-a-bad-port-in-an-ipv6-endpoint) | [#81305](https://tracker.ceph.com/issues/81305) | [ceph/ceph#72305](https://github.com/ceph/ceph/pull/72305) | ✓ |
| [Squid's radosgw fails to start when its realm search meets a realm whose period cannot be read](#squids-radosgw-fails-to-start-when-its-realm-search-meets-a-realm-whose-period-cannot-be-read) | none | [ceph/ceph#63266](https://github.com/ceph/ceph/pull/63266), [ceph/ceph#66300](https://github.com/ceph/ceph/pull/66300) |  |
| [radosgw-admin bucket rm exits 0 when it removes nothing](#radosgw-admin-bucket-rm-exits-0-when-it-removes-nothing) | none | none | ✓ |
| [radosgw's S3 ListBuckets reads neither max-buckets nor continuation-token](#radosgws-s3-listbuckets-reads-neither-max-buckets-nor-continuation-token) | [#72315](https://tracker.ceph.com/issues/72315), [#75463](https://tracker.ceph.com/issues/75463) | [ceph/ceph#64742](https://github.com/ceph/ceph/pull/64742), [ceph/ceph#67920](https://github.com/ceph/ceph/pull/67920) |  |
| [url_decode reads outside its hex table for a byte above 0x7f after "%"](#url_decode-reads-outside-its-hex-table-for-a-byte-above-0x7f-after-) | [#4755](https://tracker.ceph.com/issues/4755), [#81267](https://tracker.ceph.com/issues/81267) | [ceph/ceph#72272](https://github.com/ceph/ceph/pull/72272) |  |
| [A malformed percent-escape in the path makes radosgw serve another path](#a-malformed-percent-escape-in-the-path-makes-radosgw-serve-another-path) | [#71458](https://tracker.ceph.com/issues/71458), [#81301](https://tracker.ceph.com/issues/81301) | [ceph/ceph#72301](https://github.com/ceph/ceph/pull/72301) |  |
| [radosgw skips or never finishes every bucket-index batch on a zero rgw_bucket_index_max_aio](#radosgw-skips-or-never-finishes-every-bucket-index-batch-on-a-zero-rgw_bucket_index_max_aio) | [#81302](https://tracker.ceph.com/issues/81302) | [ceph/ceph#72302](https://github.com/ceph/ceph/pull/72302) | ✓ |
| [radosgw clamps a copy of rgw_override_bucket_index_max_shards that bucket creation never reads](#radosgw-clamps-a-copy-of-rgw_override_bucket_index_max_shards-that-bucket-creation-never-reads) | [#70980](https://tracker.ceph.com/issues/70980) | [ceph/ceph#72256](https://github.com/ceph/ceph/pull/72256) |  |
| [radosgw wraps an rgw_cache_expiry_interval above 18446744073 seconds](#radosgw-wraps-an-rgw_cache_expiry_interval-above-18446744073-seconds) | [#81218](https://tracker.ceph.com/issues/81218) | [ceph/ceph#72255](https://github.com/ceph/ceph/pull/72255) | ✓ |
| [radosgw spins or stops caching on a negative usage-log or quota interval](#radosgw-spins-or-stops-caching-on-a-negative-usage-log-or-quota-interval) | [#81226](https://tracker.ceph.com/issues/81226) | [ceph/ceph#72261](https://github.com/ceph/ceph/pull/72261) | ✓ |
| [radosgw's ARN conditions compare each ARN component with the text after it](#radosgws-arn-conditions-compare-each-arn-component-with-the-text-after-it) | none | none | ✓ |
| [radosgw takes any policy Action starting with a wildcard for every action](#radosgw-takes-any-policy-action-starting-with-a-wildcard-for-every-action) | [#81229](https://tracker.ceph.com/issues/81229) | [ceph/ceph#72262](https://github.com/ceph/ceph/pull/72262) | ✓ |
| [radosgw reads a tagging body whose root is not Tagging as an empty tag set](#radosgw-reads-a-tagging-body-whose-root-is-not-tagging-as-an-empty-tag-set) | none | none | ✓ |
| [compressor_zlib_winsize 8 writes zlib blocks radosgw cannot read back](#compressor_zlib_winsize-8-writes-zlib-blocks-radosgw-cannot-read-back) | none | none | ✓ |
| [radosgw's zstd decompress reports success on a frame that fails or decodes short](#radosgws-zstd-decompress-reports-success-on-a-frame-that-fails-or-decodes-short) | [#77334](https://tracker.ceph.com/issues/77334) | [ceph/ceph#69733](https://github.com/ceph/ceph/pull/69733) |  |
| [radosgw's zlib decompress reports success on a truncated stream](#radosgws-zlib-decompress-reports-success-on-a-truncated-stream) | none | none | ✓ |
| [radosgw lets a user take an account's email and deletes it with the account](#radosgw-lets-a-user-take-an-accounts-email-and-deletes-it-with-the-account) | none | none | ✓ |
| [radosgw's LZ4 decompress trusts its block's pair table](#radosgws-lz4-decompress-trusts-its-blocks-pair-table) | none | none | ✓ |
| [radosgw hangs or faults walking a manifest whose rule has a stripe size of 0](#radosgw-hangs-or-faults-walking-a-manifest-whose-rule-has-a-stripe-size-of-0) | none | none | ✓ |
| [radosgw divides by zero writing with an rgw_obj_stripe_size of 0](#radosgw-divides-by-zero-writing-with-an-rgw_obj_stripe_size-of-0) | none | none | ✓ |
| [cls_rgw's omap gc log loses an entry due in the same nanosecond as another](#cls_rgws-omap-gc-log-loses-an-entry-due-in-the-same-nanosecond-as-another) | none | none | ✓ |
| [radosgw's tail stripe names go negative at stripe 2^31 and repeat at 2^32](#radosgws-tail-stripe-names-go-negative-at-stripe-231-and-repeat-at-232) | none | none | ✓ |
| [radosgw's user removal reports success over a lost version race, leaving the user without its indexes](#radosgws-user-removal-reports-success-over-a-lost-version-race-leaving-the-user-without-its-indexes) | none | none | ✓ |
| [radosgw's object stat ignores a data pool it cannot resolve](#radosgws-object-stat-ignores-a-data-pool-it-cannot-resolve) | none | none | ✓ |
| [radosgw never finishes a GET on a zero rgw_get_obj_max_req_size](#radosgw-never-finishes-a-get-on-a-zero-rgw_get_obj_max_req_size) | none | none | ✓ |
| [rados_nobjects_list_seek reports the position it was given, not the one it lands at](#rados_nobjects_list_seek-reports-the-position-it-was-given-not-the-one-it-lands-at) | none | none | ✓ |
| [librados aborts the process on a listing seek after its pool is deleted](#librados-aborts-the-process-on-a-listing-seek-after-its-pool-is-deleted) | none | none | ✓ |
| [radosgw drops all but the first OIDC provider or Service principal in a statement](#radosgw-drops-all-but-the-first-oidc-provider-or-service-principal-in-a-statement) | [#76069](https://tracker.ceph.com/issues/76069) (OIDC), none (Service) | [ceph/ceph#68850](https://github.com/ceph/ceph/pull/68850) (OIDC), none (Service) |  |
| [radosgw cannot load a bucket whose entry point is from before version 8](#radosgw-cannot-load-a-bucket-whose-entry-point-is-from-before-version-8) | none | none | ✓ |
| [radosgw takes any prefix of bytes for a Range's unit](#radosgw-takes-any-prefix-of-bytes-for-a-ranges-unit) | none | [ceph/ceph#71300](https://github.com/ceph/ceph/pull/71300) |  |
| [radosgw's parse_time drops a numeric zone offset](#radosgws-parse_time-drops-a-numeric-zone-offset) | none | [ceph/ceph#20453](https://github.com/ceph/ceph/pull/20453), [ceph/ceph#34083](https://github.com/ceph/ceph/pull/34083) |  |
| [radosgw's parse_time wraps a date outside 1970 to 2106](#radosgws-parse_time-wraps-a-date-outside-1970-to-2106) | none | none | ✓ |
| [radosgw sends no response, or two status lines, when it refuses a response-* parameter](#radosgw-sends-no-response-or-two-status-lines-when-it-refuses-a-response--parameter) | none | none | ✓ |
| [radosgw terminates on a stored lz4 block too short for its pair table](#radosgw-terminates-on-a-stored-lz4-block-too-short-for-its-pair-table) | pending | pending |  |
| [Tentacle's radosgw answers EIO for an index shard its header read means to skip](#tentacles-radosgw-answers-eio-for-an-index-shard-its-header-read-means-to-skip) | none | none | ✓ |
| [cls_user reset_user_stats2 drops the stats of every page but the last](#cls_user-reset_user_stats2-drops-the-stats-of-every-page-but-the-last) | none | none | ✓ |
| [Squid's negated condition operators hold when any pair differs, and its Null ignores its values](#squids-negated-condition-operators-hold-when-any-pair-differs-and-its-null-ignores-its-values) | [#73146](https://tracker.ceph.com/issues/73146), [#74736](https://tracker.ceph.com/issues/74736) | [ceph/ceph#65606](https://github.com/ceph/ceph/pull/65606), [ceph/ceph#67188](https://github.com/ceph/ceph/pull/67188), [ceph/ceph#67214](https://github.com/ceph/ceph/pull/67214), [ceph/ceph#68444](https://github.com/ceph/ceph/pull/68444), [ceph/ceph#67213](https://github.com/ceph/ceph/pull/67213), [ceph/ceph#68445](https://github.com/ceph/ceph/pull/68445) |  |
| [radosgw's date conditions wrap past 2554 and before 1970](#radosgws-date-conditions-wrap-past-2554-and-before-1970) | none | none | ✓ |
| [radosgw never expires a presigned SigV4 URL dated before 1970](#radosgw-never-expires-a-presigned-sigv4-url-dated-before-1970) | none | none | ✓ |
| [radosgw does not check aws-chunked framing against x-amz-decoded-content-length](#radosgw-does-not-check-aws-chunked-framing-against-x-amz-decoded-content-length) | none | none | ✓ |
| [radosgw answers GetObjectTagging with 200 and no body when the tags do not decode](#radosgw-answers-getobjecttagging-with-200-and-no-body-when-the-tags-do-not-decode) | none | none | ✓ |
| [radosgw counts and pages a multipart object's parts as if their numbers had no gaps](#radosgw-counts-and-pages-a-multipart-objects-parts-as-if-their-numbers-had-no-gaps) | none | none | ✓ |
| [Squid's account admin API checks a cap type that does not exist for get and delete](#squids-account-admin-api-checks-a-cap-type-that-does-not-exist-for-get-and-delete) | [#72527](https://tracker.ceph.com/issues/72527), [#69544](https://tracker.ceph.com/issues/69544) | [ceph/ceph#65480](https://github.com/ceph/ceph/pull/65480), [ceph/ceph#66905](https://github.com/ceph/ceph/pull/66905), [ceph/ceph#66919](https://github.com/ceph/ceph/pull/66919), [ceph/ceph#71186](https://github.com/ceph/ceph/pull/71186) |  |
| [radosgw's eval_principal skips NotPrincipal for a role that Principal names, but not for a user](#radosgws-eval_principal-skips-notprincipal-for-a-role-that-principal-names-but-not-for-a-user) | none | none | ✓ |
| [radosgw's is_public judges a wildcard-principal statement against a fixed three-key environment](#radosgws-is_public-judges-a-wildcard-principal-statement-against-a-fixed-three-key-environment) | none | none | ✓ |
| [radosgw's SigV2 date check wraps the request time to 32 bits](#radosgws-sigv2-date-check-wraps-the-request-time-to-32-bits) | none | none | ✓ |
| [radosgw signs only the last value of a repeated x-amz- header](#radosgw-signs-only-the-last-value-of-a-repeated-x-amz--header) | [#75304](https://tracker.ceph.com/issues/75304) | [ceph/ceph#70459](https://github.com/ceph/ceph/pull/70459) |  |
| [radosgw's SigV2 resource lists encryption and object-lock but never signs them](#radosgws-sigv2-resource-lists-encryption-and-object-lock-but-never-signs-them) | none | none | ✓ |
| [radosgw cuts an aws-chunked trailer section by its trailers' length, not their position](#radosgw-cuts-an-aws-chunked-trailer-section-by-its-trailers-length-not-their-position) | pending | pending |  |
| [radosgw reads an aws-chunked trailer line's value only up to a second colon, and drops an empty one](#radosgw-reads-an-aws-chunked-trailer-lines-value-only-up-to-a-second-colon-and-drops-an-empty-one) | pending | pending |  |
| [radosgw's from_base64 reads before an all-'=' input](#radosgws-from_base64-reads-before-an-all--input) | none | none | ✓ |
| [radosgw reads a runtime condition's key from its last value, and terminates when that value is short](#radosgw-reads-a-runtime-conditions-key-from-its-last-value-and-terminates-when-that-value-is-short) | none | none | ✓ |
| [radosgw spins on a ranged GET of a compressed block larger than rgw_max_chunk_size](#radosgw-spins-on-a-ranged-get-of-a-compressed-block-larger-than-rgw_max_chunk_size) | none | none | ✓ |
| [radosgw renders GetObjectAttributes's NextPartNumberMarker from an unset variable when max-parts is below 1](#radosgw-renders-getobjectattributess-nextpartnumbermarker-from-an-unset-variable-when-max-parts-is-below-1) | pending | pending |  |
| [radosgw's ObjectParts gives a part without a checksum the checksum of the part before it](#radosgws-objectparts-gives-a-part-without-a-checksum-the-checksum-of-the-part-before-it) | pending | pending |  |
| [radosgw starts an aws-chunked trailer section from a stale leftover count](#radosgw-starts-an-aws-chunked-trailer-section-from-a-stale-leftover-count) | pending | pending |  |
| [radosgw's signed aws-chunked header parse ignores the key and misframes one not 15 bytes long](#radosgws-signed-aws-chunked-header-parse-ignores-the-key-and-misframes-one-not-15-bytes-long) | none | none | ✓ |
| [radosgw never compares the final aws-chunked chunk's signature and parses its line loosely](#radosgw-never-compares-the-final-aws-chunked-chunks-signature-and-parses-its-line-loosely) | [#72253](https://tracker.ceph.com/issues/72253), [#45790](https://tracker.ceph.com/issues/45790) | [ceph/ceph#64934](https://github.com/ceph/ceph/pull/64934) |  |
| [radosgw finds the aws-chunked trailer signature and trailers by substring search anywhere in its window](#radosgw-finds-the-aws-chunked-trailer-signature-and-trailers-by-substring-search-anywhere-in-its-window) | none | none | ✓ |
| [Squid's cls_rgw reshard guard and Squid's radosgw reshard wait disagree, so guard_reshard spins without waiting](#squids-cls_rgw-reshard-guard-and-squids-radosgw-reshard-wait-disagree-so-guard_reshard-spins-without-waiting) | pending | pending |  |
| [radosgw's garbage collector runs one more concurrent IO than rgw_gc_max_concurrent_io](#radosgws-garbage-collector-runs-one-more-concurrent-io-than-rgw_gc_max_concurrent_io) | pending | pending |  |
| [radosgw re-reads a resharding bucket by name, so a write can land in a bucket recreated under that name](#radosgw-re-reads-a-resharding-bucket-by-name-so-a-write-can-land-in-a-bucket-recreated-under-that-name) | pending | pending |  |
| [radosgw turns a negative rgw_gc_max_concurrent_io or rgw_gc_max_trim_chunk into a huge unsigned limit](#radosgw-turns-a-negative-rgw_gc_max_concurrent_io-or-rgw_gc_max_trim_chunk-into-a-huge-unsigned-limit) | pending | pending |  |
| [Squid's is_public counts every Allow statement whose NotPrincipal is not the wildcard](#squids-is_public-counts-every-allow-statement-whose-notprincipal-is-not-the-wildcard) | [#67047](https://tracker.ceph.com/issues/67047), [#67048](https://tracker.ceph.com/issues/67048), [#67176](https://tracker.ceph.com/issues/67176), [#67177](https://tracker.ceph.com/issues/67177) | [ceph/ceph#58686](https://github.com/ceph/ceph/pull/58686) |  |
| [radosgw ignores NotResource in a statement that also names Resource](#radosgw-ignores-notresource-in-a-statement-that-also-names-resource) | none | none | ✓ |
| [radosgw's princ_type after Policy::eval reflects the last statement evaluated, not the one that matched](#radosgws-princ_type-after-policyeval-reflects-the-last-statement-evaluated-not-the-one-that-matched) | none | none | ✓ |
| [radosgw accepts a policy statement with no Effect and evaluates it as Deny](#radosgw-accepts-a-policy-statement-with-no-effect-and-evaluates-it-as-deny) | none | none | ✓ |
| [Tentacle's radosgw aborts on a policy whose Statement is a string](#tentacles-radosgw-aborts-on-a-policy-whose-statement-is-a-string) | none | none | ✓ |
| [radosgw aborts on a stored IAM policy attr with bytes past its encoding](#radosgw-aborts-on-a-stored-iam-policy-attr-with-bytes-past-its-encoding) | pending | pending |  |
| [radosgw's ARN ordering is not a strict weak ordering](#radosgws-arn-ordering-is-not-a-strict-weak-ordering) | pending | pending |  |
| [radosgw's policy parser forgets a statement's keys when an object in an array closes](#radosgws-policy-parser-forgets-a-statements-keys-when-an-object-in-an-array-closes) | pending | pending |  |
| [radosgw refuses a JSON true, false or null in a policy with "No error?"](#radosgw-refuses-a-json-true-false-or-null-in-a-policy-with-no-error) | none | none | ✓ |
| [Squid's PUT refuses a quoted If-Match that matches the object's ETag](#squids-put-refuses-a-quoted-if-match-that-matches-the-objects-etag) | [#64439](https://tracker.ceph.com/issues/64439), [#80924](https://tracker.ceph.com/issues/80924) | [ceph/ceph#63348](https://github.com/ceph/ceph/pull/63348), [ceph/ceph#65949](https://github.com/ceph/ceph/pull/65949), [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932) |  |
| [Squid's write conditions fail an If-None-Match ETag on a missing key and skip a head without a write tag](#squids-write-conditions-fail-an-if-none-match-etag-on-a-missing-key-and-skip-a-head-without-a-write-tag) | [#68183](https://tracker.ceph.com/issues/68183), [#80925](https://tracker.ceph.com/issues/80925), [#80906](https://tracker.ceph.com/issues/80906), [#80922](https://tracker.ceph.com/issues/80922), [#80907](https://tracker.ceph.com/issues/80907), [#80898](https://tracker.ceph.com/issues/80898) | [ceph/ceph#63348](https://github.com/ceph/ceph/pull/63348), [ceph/ceph#65949](https://github.com/ceph/ceph/pull/65949), [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932) |  |
| [radosgw's send_split_chain repeats an object in its remainder and loops on an object too large for an entry](#radosgws-send_split_chain-repeats-an-object-in-its-remainder-and-loops-on-an-object-too-large-for-an-entry) | none | none | ✓ |
| [Tentacle's delete_objs_inline puts every ref through the first object's pool, without its locator](#tentacles-delete_objs_inline-puts-every-ref-through-the-first-objects-pool-without-its-locator) | none | none | ✓ |
| [radosgw files a 404 on an existing bucket under the usage bucket "-", and never logs a missing bucket](#radosgw-files-a-404-on-an-existing-bucket-under-the-usage-bucket---and-never-logs-a-missing-bucket) | none | none | ✓ |
| [radosgw files the first second of each hour's usage under the hour before](#radosgw-files-the-first-second-of-each-hours-usage-under-the-hour-before) | none | none | ✓ |
| [cls_rgw's usage reads and trims take in other users' records](#cls_rgws-usage-reads-and-trims-take-in-other-users-records) | none; [#72593](https://tracker.ceph.com/issues/72593) (the `0` half) | none; [ceph/ceph#65329](https://github.com/ceph/ceph/pull/65329) (the `0` half) | ✓ |
| [radosgw cuts X-Forwarded-For only for an rgw_remote_addr_param written in capitals](#radosgw-cuts-x-forwarded-for-only-for-an-rgw_remote_addr_param-written-in-capitals) | none | none | ✓ |
| [radosgw ignores a public-access block that does not decode](#radosgw-ignores-a-public-access-block-that-does-not-decode) | none | none | ✓ |
| [Squid's DeleteObjectTagging stamps the object with the epoch plus one nanosecond](#squids-deleteobjecttagging-stamps-the-object-with-the-epoch-plus-one-nanosecond) | pending | pending |  |
| [radosgw never takes its OSD-filtered delimiter path](#radosgw-never-takes-its-osd-filtered-delimiter-path) | pending | pending |  |
| [radosgw's ListObjectsV2 with versions renders a malformed ListVersionsResult](#radosgws-listobjectsv2-with-versions-renders-a-malformed-listversionsresult) | pending | pending |  |
| [radosgw's listing checks a plain object named like a multipart meta object in the wrong pool](#radosgws-listing-checks-a-plain-object-named-like-a-multipart-meta-object-in-the-wrong-pool) | pending | pending |  |
| [A delimiter starting with an underscore hides the objects whose names start with one](#a-delimiter-starting-with-an-underscore-hides-the-objects-whose-names-start-with-one) | pending | pending |  |
| [radosgw creates a multipart upload's meta object without the exclusive create its flags ask for](#radosgw-creates-a-multipart-uploads-meta-object-without-the-exclusive-create-its-flags-ask-for) | none | none | ✓ |
| [radosgw keeps every modified bucket for good when its quota threads are off](#radosgw-keeps-every-modified-bucket-for-good-when-its-quota-threads-are-off) | none | none |  |
| [Tentacle's DeleteObject removes the head without checking its write tag](#tentacles-deleteobject-removes-the-head-without-checking-its-write-tag) | [#80898](https://tracker.ceph.com/issues/80898), [#80906](https://tracker.ceph.com/issues/80906) | [ceph/ceph#72100](https://github.com/ceph/ceph/pull/72100), [ceph/ceph#72096](https://github.com/ceph/ceph/pull/72096), [ceph/ceph#72101](https://github.com/ceph/ceph/pull/72101) |  |
| [radosgw evaluates a tag-conditioned policy without the tags it fails to read](#radosgw-evaluates-a-tag-conditioned-policy-without-the-tags-it-fails-to-read) | pending | pending |  |
| [A failed UploadPart shrinks radosgw's quota cache](#a-failed-uploadpart-shrinks-radosgws-quota-cache) | none | none | ✓ |
| [Tentacle fails an UploadPart that sends If-None-Match: * for a part in the bucket placement's pool](#tentacle-fails-an-uploadpart-that-sends-if-none-match--for-a-part-in-the-bucket-placements-pool) | [#68183](https://tracker.ceph.com/issues/68183) | [ceph/ceph#63348](https://github.com/ceph/ceph/pull/63348), [ceph/ceph#65949](https://github.com/ceph/ceph/pull/65949), [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932) |  |
| [radosgw's UploadPartCopy can assemble a part from two versions of its source](#radosgws-uploadpartcopy-can-assemble-a-part-from-two-versions-of-its-source) | none | none | ✓ |
| [radosgw registers a part whose head write lost a race and then removes its data](#radosgw-registers-a-part-whose-head-write-lost-a-race-and-then-removes-its-data) | none | none | ✓ |
| [radosgw's gc removes queue entries by count, so a pass drops entries not yet due that precede due ones](#radosgws-gc-removes-queue-entries-by-count-so-a-pass-drops-entries-not-yet-due-that-precede-due-ones) | none | none | ✓ |
| [radosgw's gc worker runs its passes without pause on a non-positive rgw_gc_processor_period](#radosgws-gc-worker-runs-its-passes-without-pause-on-a-non-positive-rgw_gc_processor_period) | none | none | ✓ |
| [radosgw's gc processor faults on a chain object without a pool](#radosgws-gc-processor-faults-on-a-chain-object-without-a-pool) | none | none | ✓ |
| [radosgw's gc stalls a converted shard behind an entry it cannot free](#radosgws-gc-stalls-a-converted-shard-behind-an-entry-it-cannot-free) | [#68169](https://tracker.ceph.com/issues/68169), [#77098](https://tracker.ceph.com/issues/77098) | [ceph/ceph#59902](https://github.com/ceph/ceph/pull/59902), [ceph/ceph#70395](https://github.com/ceph/ceph/pull/70395), [ceph/ceph#70396](https://github.com/ceph/ceph/pull/70396) |  |
| [radosgw's gc takes a failed omap listing of a converted shard for an empty one](#radosgws-gc-takes-a-failed-omap-listing-of-a-converted-shard-for-an-empty-one) | none | none | ✓ |
| [A negative or overlong rgw_gc_obj_min_wait makes radosgw's gc free overwritten tails at its next pass](#a-negative-or-overlong-rgw_gc_obj_min_wait-makes-radosgws-gc-free-overwritten-tails-at-its-next-pass) | none | none | ✓ |
| [radosgw's gc can remove a queue page twice when a pass outlives its shard lock](#radosgws-gc-can-remove-a-queue-page-twice-when-a-pass-outlives-its-shard-lock) | none | none | ✓ |
| [Tentacle's radosgw never applies rgwx-perm-check-uid, so a user-mode sync pipe's source read goes unchecked](#tentacles-radosgw-never-applies-rgwx-perm-check-uid-so-a-user-mode-sync-pipes-source-read-goes-unchecked) | none | none | ✓ |
| [radosgw grants a signed CORS preflight without comparing its signature](#radosgw-grants-a-signed-cors-preflight-without-comparing-its-signature) | [#64308](https://tracker.ceph.com/issues/64308) | [ceph/ceph#55458](https://github.com/ceph/ceph/pull/55458), [ceph/ceph#59977](https://github.com/ceph/ceph/pull/59977) | ✓ (the rate-limit charge) |
| [radosgw's copy drops its tail references after a head write that timed out](#radosgws-copy-drops-its-tail-references-after-a-head-write-that-timed-out) | [#80899](https://tracker.ceph.com/issues/80899) | [ceph/ceph#72098](https://github.com/ceph/ceph/pull/72098) |  |
| [radosgw's copy keeps its tail references when its head write loses a race](#radosgws-copy-keeps-its-tail-references-when-its-head-write-loses-a-race) | [#80899](https://tracker.ceph.com/issues/80899), [#80902](https://tracker.ceph.com/issues/80902) | [ceph/ceph#72098](https://github.com/ceph/ceph/pull/72098) |  |
| [Squid labels a copy to another storage class with its source's class](#squid-labels-a-copy-to-another-storage-class-with-its-sources-class) | [#23264](https://tracker.ceph.com/issues/23264) | [ceph/ceph#63794](https://github.com/ceph/ceph/pull/63794), [ceph/ceph#69277](https://github.com/ceph/ceph/pull/69277) |  |
| [radosgw's copy of an object onto itself can land its old manifest on tails queued for the GC](#radosgws-copy-of-an-object-onto-itself-can-land-its-old-manifest-on-tails-queued-for-the-gc) | pending | pending |  |
| [radosgw fails every tail-sharing copy on a zero rgw_max_copy_obj_concurrent_io](#radosgw-fails-every-tail-sharing-copy-on-a-zero-rgw_max_copy_obj_concurrent_io) | pending | pending |  |
| [radosgw's bucket creation retry leaks the abandoned instance and repeats its log layout](#radosgws-bucket-creation-retry-leaks-the-abandoned-instance-and-repeats-its-log-layout) | none | none | ✓ |
| [radosgw reports a bucket whose owner link failed as created](#radosgw-reports-a-bucket-whose-owner-link-failed-as-created) | none | none | ✓ |
| [radosgw's bucket delete answers success when its instance removal loses a race](#radosgws-bucket-delete-answers-success-when-its-instance-removal-loses-a-race) | [#69738](https://tracker.ceph.com/issues/69738) | [ceph/ceph#61684](https://github.com/ceph/ceph/pull/61684) |  |
| [radosgw's bucket delete unlinks a bucket re-created under the same name from its owner](#radosgws-bucket-delete-unlinks-a-bucket-re-created-under-the-same-name-from-its-owner) | none | none | ✓ |
| [Tentacle cannot delete an indexless bucket it created](#tentacle-cannot-delete-an-indexless-bucket-it-created) | none | none | ✓ |
| [radosgw takes a SigV4 request without x-amz-content-sha256 as UNSIGNED-PAYLOAD](#radosgw-takes-a-sigv4-request-without-x-amz-content-sha256-as-unsigned-payload) | pending | pending |  |
| [rgw_s3_auth_disable_signature_url denies header-signed requests too](#rgw_s3_auth_disable_signature_url-denies-header-signed-requests-too) | none | none | ✓ |
| [radosgw keeps repeated SigV4 query keys in arrival order](#radosgw-keeps-repeated-sigv4-query-keys-in-arrival-order) | pending | pending |  |
| [radosgw never checks a SigV4 credential scope's date, region or service](#radosgw-never-checks-a-sigv4-credential-scopes-date-region-or-service) | pending | pending |  |
| [radosgw's ListMultipartUploads drops its common prefixes and the uploads under them](#radosgws-listmultipartuploads-drops-its-common-prefixes-and-the-uploads-under-them) | none | none | ✓ |
| [radosgw's ListMultipartUploads applies the prefix and the delimiter to the meta object's name](#radosgws-listmultipartuploads-applies-the-prefix-and-the-delimiter-to-the-meta-objects-name) | none | none | ✓ |
| [Tentacle's DeleteObjects asks for MFA for the keys that need none and not for the versions that do](#tentacles-deleteobjects-asks-for-mfa-for-the-keys-that-need-none-and-not-for-the-versions-that-do) | [#77869](https://tracker.ceph.com/issues/77869), [#78708](https://tracker.ceph.com/issues/78708) | [ceph/ceph#69821](https://github.com/ceph/ceph/pull/69821) |  |
| [radosgw's parse_date writes a NUL one byte past its format buffer](#radosgws-parse_date-writes-a-nul-one-byte-past-its-format-buffer) | none | none | ✓ |
| [radosgw lets a public ACL through a block of public ACLs on PutObject's grant headers, CopyObject and CreateMultipartUpload](#radosgw-lets-a-public-acl-through-a-block-of-public-acls-on-putobjects-grant-headers-copyobject-and-createmultipartupload) | none | none | ✓ |
| [RESTArgs::get_string url-decodes an admin argument a second time](#restargsget_string-url-decodes-an-admin-argument-a-second-time) | pending | pending |  |
| [radosgw's integer argument readers refuse their type's maximum and truncate to 32 bits](#radosgws-integer-argument-readers-refuse-their-types-maximum-and-truncate-to-32-bits) | pending | pending |  |
| [HTMLFormatter formats a long value from an ended va_list](#htmlformatter-formats-a-long-value-from-an-ended-va_list) | pending | pending |  |
| [radosgw drops a role or session policy that does not parse, and evaluates the request without it](#radosgw-drops-a-role-or-session-policy-that-does-not-parse-and-evaluates-the-request-without-it) | none | none | ✓ |
| [A refused CompleteMultipartUpload drops its parts' index entries](#a-refused-completemultipartupload-drops-its-parts-index-entries) | [#80907](https://tracker.ceph.com/issues/80907) | [ceph/ceph#72109](https://github.com/ceph/ceph/pull/72109) |  |
| [A multipart completion whose meta object outlives its head write lets a retry or an abort delete the object's data](#a-multipart-completion-whose-meta-object-outlives-its-head-write-lets-a-retry-or-an-abort-delete-the-objects-data) | [#80896](https://tracker.ceph.com/issues/80896), [#75375](https://tracker.ceph.com/issues/75375) | [ceph/ceph#72103](https://github.com/ceph/ceph/pull/72103) |  |
| [radosgw's CompleteMultipartUpload does not keep its lock past rgw_mp_lock_max_time](#radosgws-completemultipartupload-does-not-keep-its-lock-past-rgw_mp_lock_max_time) | [#75375](https://tracker.ceph.com/issues/75375) | [ceph/ceph#67696](https://github.com/ceph/ceph/pull/67696) |  |
| [radosgw sends several GC chains under one tag, and an omap-era gc shard keeps only the last](#radosgw-sends-several-gc-chains-under-one-tag-and-an-omap-era-gc-shard-keeps-only-the-last) | none | none | ✓ |
| [A CompleteMultipartUpload that loses its head write's race leaks its parts](#a-completemultipartupload-that-loses-its-head-writes-race-leaks-its-parts) | pending | pending |  |
| [radosgw's PutBucketAcl takes any canned ACL whose name holds "bucket" for private](#radosgws-putbucketacl-takes-any-canned-acl-whose-name-holds-bucket-for-private) | pending | pending |  |
| [Tentacle's radosgw terminates on a GET or HEAD of an object whose restore attr does not decode](#tentacles-radosgw-terminates-on-a-get-or-head-of-an-object-whose-restore-attr-does-not-decode) | none | none | ✓ |
| [radosgw sends the ACL document as a body after the headers of a HEAD ?acl](#radosgw-sends-the-acl-document-as-a-body-after-the-headers-of-a-head-acl) | none | none | ✓ |
| [radosgw stores a POST upload's x-amz-meta fields with their CR and LF and sends them raw on GET](#radosgw-stores-a-post-uploads-x-amz-meta-fields-with-their-cr-and-lf-and-sends-them-raw-on-get) | none | none | ✓ |
| [radosgw's AbortMultipartUpload queues the parts for the GC before it removes the upload](#radosgws-abortmultipartupload-queues-the-parts-for-the-gc-before-it-removes-the-upload) | [#80896](https://tracker.ceph.com/issues/80896) | none |  |
| [radosgw's bucket delete aborts each page of multipart uploads again on every later page](#radosgws-bucket-delete-aborts-each-page-of-multipart-uploads-again-on-every-later-page) | pending | pending |  |
| [radosgw's retried bucket write can land in a bucket re-created under the same name](#radosgws-retried-bucket-write-can-land-in-a-bucket-re-created-under-the-same-name) | none | none | ✓ |
| [radosgw's PutBucketPolicy retry writes back the bucket attrs its request started with](#radosgws-putbucketpolicy-retry-writes-back-the-bucket-attrs-its-request-started-with) | [#51572](https://tracker.ceph.com/issues/51572) | none |  |
| [radosgw's PutBucketAcl answers success when its write loses a race](#radosgws-putbucketacl-answers-success-when-its-write-loses-a-race) | [#16930](https://tracker.ceph.com/issues/16930) | none |  |
| [Tentacle never stores the confirmation of x-amz-confirm-remove-self-bucket-access](#tentacle-never-stores-the-confirmation-of-x-amz-confirm-remove-self-bucket-access) | none; [#66177](https://tracker.ceph.com/issues/66177) (the feature) | none; [ceph/ceph#57629](https://github.com/ceph/ceph/pull/57629) (the feature) | ✓ |
| [radosgw's PutBucketAcl and PutObjectAcl refuse a request without a Content-Length that they mean to accept](#radosgws-putbucketacl-and-putobjectacl-refuse-a-request-without-a-content-length-that-they-mean-to-accept) | [#43148](https://tracker.ceph.com/issues/43148) | [ceph/ceph#31987](https://github.com/ceph/ceph/pull/31987) | ✓ (the dead guard) |
| [radosgw lets an upload id address another key's multipart upload](#radosgw-lets-an-upload-id-address-another-keys-multipart-upload) | none | none | ✓ |
| [radosgw holds a copy-source range's bounds in an off_t, so a bound past 2^63 reads as a suffix](#radosgw-holds-a-copy-source-ranges-bounds-in-an-off_t-so-a-bound-past-263-reads-as-a-suffix) | none | none | ✓ |
| [RGWOp::read_all_input ignores its allow_chunked argument](#rgwopread_all_input-ignores-its-allow_chunked-argument) | none | none | ✓ |
| [Removing an account's root user leaves its name in the account's users index](#removing-an-accounts-root-user-leaves-its-name-in-the-accounts-users-index) | none | none | ✓ |
| [radosgw cannot remove an account user whose users index entry is gone](#radosgw-cannot-remove-an-account-user-whose-users-index-entry-is-gone) | none | none | ✓ |
| [radosgw's admin user info shows the Swift TempURL keys to a caller it withholds keys from](#radosgws-admin-user-info-shows-the-swift-tempurl-keys-to-a-caller-it-withholds-keys-from) | none | none | ✓ |
| [A stale bucket list entry blocks a user's removal for good](#a-stale-bucket-list-entry-blocks-a-users-removal-for-good) | none | none | ✓ |
| [radosgw stores request credentials as object attrs](#radosgw-stores-request-credentials-as-object-attrs) | [#65460](https://tracker.ceph.com/issues/65460) | [ceph/ceph#63794](https://github.com/ceph/ceph/pull/63794), [ceph/ceph#69277](https://github.com/ceph/ceph/pull/69277) |  |
| [radosgw's CopyObject stores an object lock on a bucket without object lock](#radosgws-copyobject-stores-an-object-lock-on-a-bucket-without-object-lock) | none | none | ✓ |
| [radosgw's Swift key modify rebuilds the key](#radosgws-swift-key-modify-rebuilds-the-key) | pending | pending | ✓ |
| [radosgw's quota set stores garbage for an unparsable max-size-kb](#radosgws-quota-set-stores-garbage-for-an-unparsable-max-size-kb) | pending | pending | ✓ |
| [radosgw's admin API takes an unparsable boolean argument as its default](#radosgws-admin-api-takes-an-unparsable-boolean-argument-as-its-default) | pending | pending | ✓ |
| [radosgw lets another user take an inactive access key's id](#radosgw-lets-another-user-take-an-inactive-access-keys-id) | pending | pending | ✓ |
| [radosgw's ListParts drops its refusal of an empty upload id](#radosgws-listparts-drops-its-refusal-of-an-empty-upload-id) | pending | pending | ✓ |
| [radosgw's CompleteMultipartUpload Location has no scheme under a configured domain](#radosgws-completemultipartupload-location-has-no-scheme-under-a-configured-domain) | pending | pending | ✓ |
| [radosgw's retried bucket writes are not authorized again](#radosgws-retried-bucket-writes-are-not-authorized-again) | pending | pending | ✓ |
| [radosgw's bucket link overwrites the entry point of the bucket it renames onto](#radosgws-bucket-link-overwrites-the-entry-point-of-the-bucket-it-renames-onto) | pending | pending | ✓ |

A ✓ under Found by us marks a defect first found by the project's own sessions, the repository owner's Claude Code sessions such as rgw-go, rgw-rs and rgw-bug-reproduction, with no earlier upstream report or fix PR.

In the Upstream issues and Upstream fix PRs columns, "pending" means no prior-art search has run yet, and "none" means a search ran and found nothing.

Every new entry adds its row to this table, in document order.

## Squid does not guard listing-time index suggestions against resharding

- **Kind:** defect, fixed in Tentacle.
- **Evidence:**
  - Listing sends `cls_rgw_suggest_changes` to repair stale index entries, and
    at v19.2.6 it sends them without the reshard guard (`rgw_rados.cc:9902`
    and `:10138`).
  - Ceph commit 461be1cd3d5, "rgw/rados: guard against dir suggest during
    reshard", adds the guard: "no changes to the bucket index should be
    allowed while resharding". It is in v20.0.0 and later (v20.2.4
    `rgw_rados.cc:10823` and `:11061`).
  - It is not on the squid branch as of a742f50 (2026-09-03).
- **Releases:** Squid, every release through v19.2.6.
- **rgw-go:** follows the cluster's release. Its Tentacle-level listing guards
  its suggestions; its Squid-level listing matches radosgw and does not.
  `docs/exclusions.md` states the guard per release.
- **Upstream:** [#81000](https://tracker.ceph.com/issues/81000), which we
  filed, requests the squid backport of 461be1cd3d5, which came with
  [ceph/ceph#59609](https://github.com/ceph/ceph/pull/59609). The backport,
  [ceph/ceph#72254](https://github.com/ceph/ceph/pull/72254), a draft, is a
  clean cherry-pick of 461be1cd3d5 that keeps Casey Bodley as its author. It
  is open, and its backport audit fails: the audit needs a source tracker for
  ceph/ceph#59609, which merged in 2024 without one.
  [#81217](https://tracker.ceph.com/issues/81217), created for
  ceph/ceph#59609 with Backport set to squid, now supplies that link, and a
  comment on ceph/ceph#72254 points to it. Moving #81217 to Pending Backport
  and rerunning or overriding the audit are reserved to the Ceph release
  team, so the audit stays red until they act.
- **Found:** final phase 0 review, 2026-09-26.

## check_disk_state removes a multipart part's index entry from the wrong shard

- **Kind:** defect, unfixed through main, with a fix in review.
- **Evidence:** when a listing reconciles a head that exists, v19.2.6's
  `check_disk_state` walks the head's manifest and, for each location in the
  multipart namespace, calls `delete_obj_index` on an `rgw_obj` rebuilt by
  `raw_obj_to_obj` (`rgw_rados.cc:10413-10429`). That helper sets only the
  bucket and the key (`svc_tier_rados.h:131-143`), so the object's
  `index_hash_source` is empty and its shard is chosen by the part's own
  name (`get_hash_object`, `rgw_obj_types.h:540-541`). The part writer
  indexed the part under the upload's key instead
  (`head_obj.index_hash_source = target_obj.key.name`,
  `rgw_putobj_processor.cc:467`). On a bucket with more than one index
  shard the delete usually reaches a shard that holds no such entry, where
  `bucket_complete_op` logs "not on disk, no action" and changes nothing
  (`cls_rgw.cc:1135-1140`), leaving the part's entry behind. The same code is
  at v20.2.4 and on main (`rgw_putobj_processor.cc:501`).
  - Reproduced on v19.2.6 and v20.2.4 with a three-part upload on an
    11-shard bucket: each part's delete went to the shard its own name
    hashes to and logged "not on disk, no action", and the entries stayed
    on the upload's shard. On a bucket resharded to one shard every entry
    was removed. Each part misses on its own: its delete reaches the
    upload's shard only by chance, about one time in 11 (6 of 48 across the
    runs), and then removes the entry.
  - A listing runs the sweep for an entry with a pending op or one that a
    force-check filter selects (`rgw_rados.cc:9820-9834`). Three triggers
    reached it and left entries behind: `radosgw-admin bucket list` on a
    head with a pending op; S3 ListObjectsV2, where radosgw itself runs it;
    and `radosgw-admin bucket check --check-objects --fix`, but only while
    the upload's `.meta` entry is still indexed.
  - Without `--fix`, `bucket check --check-objects` never reaches the sweep:
    `check_object_index` returns -EINVAL before it lists anything
    (`rgw_bucket.cc:431-434`), and radosgw-admin discards the error
    (`rgw_admin.cc:8742`).
  - With `--fix` and no `.meta` entry, `check_bad_index_multipart` runs
    first (`rgw_bucket.cc:1260`) and removes every part entry of the upload
    through `remove_objs_from_index`, which hashes each part by its upload's
    key (`rgw_rados.cc:10278`). The sweep's deletes then find nothing, which
    hides the defect.
- **Releases:** every release since bucket index sharding added the hash
  source (8a04c0a61bc, first in v0.92); the sweep itself dates from
  e5dc46f6aa9 (2012) and has only been refactored since. Checked at v19.2.6,
  v20.2.4 and main.
- **rgw-go:** reproduces it: the listing's reconciliation sends radosgw's
  bytes, wrong shard included, so the index a shared zone sees is the one
  radosgw would leave (`sweepParts` and `deleteObjIndex`,
  `internal/driver/list.go` and `internal/driver/delete.go`).
- **Upstream:** [#81121](https://tracker.ceph.com/issues/81121), filed after a
  full-text tracker and all-time pull-request search (2026-09-29) found no
  report or fix. The same wrong-shard mistake with multipart entries was
  fixed at other call sites: resharding
  ([#43583](https://tracker.ceph.com/issues/43583),
  [ceph/ceph#32617](https://github.com/ceph/ceph/pull/32617)), `bi put`
  ([#53248](https://tracker.ceph.com/issues/53248),
  [ceph/ceph#43908](https://github.com/ceph/ceph/pull/43908)) and `bucket
  check --fix` ([#53874](https://tracker.ceph.com/issues/53874),
  [ceph/ceph#46030](https://github.com/ceph/ceph/pull/46030)). The last is
  the bug that journals of [#16767](https://tracker.ceph.com/issues/16767)
  and [#44660](https://tracker.ceph.com/issues/44660) describe, where `bucket
  check --check-objects --fix` removed leftover part entries only on
  unsharded buckets. `bucket check --fix` removes the part entries of an
  upload with no `.meta` entry through `remove_objs_from_index`, which until
  the fix, 0521c2ae830, sent every removal to `.dir.<bucket_id>`, the index
  object only an unsharded bucket has (`rgw_rados.cc:9111` and `:9132`,
  `svc_bi_rados.cc:97-119` at 0521c2ae830^). The fix is in v18.0.0 and
  later, and on quincy from v17.2.8. #53874's pull request field names
  [ceph/ceph#44580](https://github.com/ceph/ceph/pull/44580), an earlier
  version of the fix that was closed unmerged. #16767 and #44660 were closed by
  [ceph/ceph#49709](https://github.com/ceph/ceph/pull/49709), which fixed
  another cause.
  The fix in review, which we opened, is
  [ceph/ceph#72306](https://github.com/ceph/ceph/pull/72306) (draft). Before
  removing a part's index entry, `check_disk_state` takes the index hash
  source from the head object's name, so the removal goes to the shard the
  writer indexed the part on.
- **Found:** phase 1 plan review, 2026-09-28; verified 2026-09-29;
  reproduced 2026-09-29 on disposable Squid and Tentacle clusters.

## cls_rgw complete_op writes a stale epoch back when it cancels

- **Kind:** defect, a regression; unfixed through main.
- **Evidence:** at v19.2.6 `bucket_complete_op` turns a completion whose
  epoch is not newer than the entry's into a cancel (`cls_rgw.cc:1082-1086`),
  but first copies the op's version onto the entry (`:1094`), and the cancel
  branch writes the entry back (`:1115-1117`). The entry's `ver.epoch` falls
  back to the stale value, so a later completion whose epoch lies between the
  two passes the check and overwrites newer metadata: completions at epochs
  10, 5 and 7, in that order, leave 7's metadata indexed. The code is the same
  at v20.2.4 (`:1217-1229`) and on main (`:1228-1240`, 2026-09-28).
  - The same copy breaks radosgw's explicit cancel. `cls_obj_complete_cancel`
    sends pool -1 and epoch 0 (`rgw_rados.cc:9559-9561`), which resets the
    entry's version to -1:0; the next completion then fails the pool
    comparison and is applied whatever its epoch, so completion 10, a cancel,
    then completion 5 leaves 5's metadata indexed.
  - Reproduced on v19.2.6 and v20.2.4, with identical results, by direct
    class calls on a fresh index object: after completions 10 and 5 the entry
    holds 10's metadata under epoch 5, a completion at 7 then replaces it with
    7's, and the bucket header stats follow the index.
  - radosgw reaches it through racing PUTs of one key: the head write's epoch
    is the RADOS version it returns (`rgw_rados.cc:3300`, `:3320`),
    completions are sent asynchronously (`:9518`), and a failed write sends
    the cancel (`:3371-3374`).
  - It is a regression. Before 8b27472bbd8, "cls/rgw: index cancelation
    still cleans up remove_objs" (ceph/ceph#43854), the cancel branch wrote
    the entry back and returned before the copy (v16.2.7 `cls_rgw.cc:1017`
    and `:1019`, ahead of `entry.ver = op.ver` at `:1026`); that commit moved
    the copy above the cancel branch.
- **Releases:** v17.2.0 and later, and pacific from v16.2.12, which carries
  the backport 8c67a931c9e; checked at v19.2.6, v20.2.4 and main.
- **rgw-go:** meets it as radosgw does until a release carries the class
  fix: the class decides, so no client can work around it. By the owner's
  decision, phase 1's cancel (unit W) sends radosgw's -1:0 version, so
  rgw-go meets the explicit-cancel case too.
- **Upstream:** [#80894](https://tracker.ceph.com/issues/80894), filed
  2026-09-26 with the same analysis, both triggers and the regression from
  8b27472bbd8; its backports are tentacle and umbrella. Our
  [#81002](https://tracker.ceph.com/issues/81002) was closed as its
  duplicate. The fix under review is
  [ceph/ceph#72097](https://github.com/ceph/ceph/pull/72097);
  [ceph/ceph#72162](https://github.com/ceph/ceph/pull/72162), a second fix,
  was closed as a duplicate of it.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-008); verified 2026-09-27;
  reproduced 2026-09-28 on disposable Squid and Tentacle clusters.

## cls_rgw encodes a packed value of exactly 65536 as 0

- **Kind:** defect, unfixed through main, with a fix in review.
- **Evidence:** at v19.2.6 `encode_packed_val` uses the two-byte form for
  values up to and including 0x10000 and writes `(uint16_t)val`
  (`cls_rgw_types.h:272-275`), so 65536 is stored, and decodes, as 0. It
  encodes the pool and epoch of `rgw_bucket_entry_ver` (`:343-344`) and
  `index_ver` in index entries (`:408`) and bilog entries (`:623`). An index
  entry also stores `ver.epoch` raw (`:402`), but its decoder overwrites that
  with the packed copy (`:418` and `:426`). The bound dates from b1578ba705a
  (v0.67) and is unchanged on main (`cls_rgw_types.h:285`).
  - Reproduced on v19.2.6 and v20.2.4: an index entry written with
    `radosgw-admin bi put` and `ver.epoch` 65535 or 65537 reads back intact
    through `bi list`, and one written with 65536 reads back as epoch 0.
    Each release's ceph-dencoder decodes the two-byte encoding of 65536 as
    epoch 0 and a four-byte encoding as 65536. It is the only affected value:
    the two-byte branch takes 0x100 through 0x10000, and only 0x10000 does
    not fit in 16 bits.
- **Releases:** every release.
- **rgw-go:** reproduces it for byte identity. `encodePacked` in
  `internal/cls/rgw/types_key.go` truncates 0x10000 to 0, and
  `fixtures_test.go` pins the bytes.
- **Upstream:** [#80995](https://tracker.ceph.com/issues/80995); its
  confirmation note carries the live output from both releases and a C++
  reproducer, `repro_packed_val.cc`.
  The fix in review, which we opened, is
  [ceph/ceph#72307](https://github.com/ceph/ceph/pull/72307) (draft). The
  bound becomes `< 0x10000`, so 65536 takes the four-byte form. Only encoding
  changes, since decoding follows the width tag, so the format is unchanged;
  a value already stored as 0 stays 0.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-009); verified 2026-09-27;
  reproduced 2026-09-29 on disposable Squid and Tentacle clusters.

## Squid before 19.2.3 refuses a delete marker on top of a delete marker

- **Kind:** defect, fixed in v19.2.3 and v20.1.0.
- **Evidence:** at v19.2.2 `bucket_link_olh` answers ENOENT, and links
  nothing, for a delete marker on a new instance when the OLH already points
  at a delete marker (`cls_rgw.cc:1676-1692`). The check came with
  69d7589fb13 (v17.1.0; in Pacific from v16.2.6 as 1e575378b00) and is in
  every Reef release through v18.2.8. Ceph commit 65e3e9b5888, "rgw: revert
  PR #41897 to allow multiple delete markers to be created"
  (ceph/ceph#54957), removes it from v20.1.0 on; its backport 9cca4fd435a
  (ceph/ceph#62740) is in v19.2.3 and later.
- **Releases:** on Squid, v19.2.0 through v19.2.2, all below rgw-go's floor.
  The check runs in the OSD, so the OSD's release decides.
- **rgw-go:** unaffected. Every release it supports, v19.2.6 and later on
  Squid, carries the fix, so phase 2's versioning needs no handling for it.
- **Upstream:** fixed by
  [ceph/ceph#54957](https://github.com/ceph/ceph/pull/54957), and on squid by
  [ceph/ceph#62740](https://github.com/ceph/ceph/pull/62740); no tracker
  issue.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-007); verified 2026-09-27.

## cls_rgw usage trim never removes a payer-keyed record

- **Kind:** defect, fixed in v21.0.0 and deliberately not backported.
- **Evidence:** at v19.2.6 `user_usage_log_add` keys a record by its payer
  when it has one (`cls_rgw.cc:3583`), but the trim callback removes the keys
  built from the owner (`:3761`). A payer-keyed record in the trim range is
  therefore found on every call and never removed, so the call never answers
  ENODATA (`:3799-3800`); a trim by the payer removes the owner's record for
  the same hour and bucket instead, if there is one. radosgw repeats a trim
  until ENODATA with no bound (`rgw_rados.cc:10197-10211`), so
  `radosgw-admin usage trim` and the admin API's trim spin. Ceph commit
  674d42d9023 (ceph/ceph#65329) builds the keys from the payer; it is in
  v21.0.0 and later (v21.1.0 `cls_rgw.cc:4403`) and on neither the squid nor
  the tentacle branch.
- **Releases:** every Squid and Tentacle release, through v19.2.6 and v20.2.4.
- **rgw-go:** `internal/cls/rgw` marshals `user_usage_log_trim` but runs no
  trim loop yet. Phase 1's usage trim must bound its loop, since on Squid and
  Tentacle OSDs ENODATA may never come.
- **Upstream:** [#72593](https://tracker.ceph.com/issues/72593), fixed by
  [ceph/ceph#65329](https://github.com/ceph/ceph/pull/65329). Upstream decided
  not to backport the fix, because it changes how the usage log works; our
  backport request, [#80999](https://tracker.ceph.com/issues/80999), was
  closed as a duplicate of #72593.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-005); verified 2026-09-27.

## cls_rgw usage trim with a bucket filter stalls behind 1000 other records

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 each `user_usage_log_trim` call starts at the
  beginning of its range, since the request carries no marker
  (`cls_rgw.cc:3791`), scans at most 1000 keys (`:3794-3795`) and skips
  records of other buckets (`:3687-3688`). When those 1000 keys hold no record
  of the bucket and more keys follow, the call answers 0 rather than ENODATA
  (`:3799-3800`), and so does every later call, since nothing was removed.
  `radosgw-admin usage trim --bucket` trims by the bucket's owner and name
  (`rgw_sal_rados.cc:799-806`) through the unbounded loop of the previous
  entry, so it spins once the owner has more than 1000 records of other
  buckets in the range; records past them are never reached. The logic is
  unchanged at v20.2.4, v21.1.0 and main.
- **Releases:** every release checked, v19.2.2 through main.
- **rgw-go:** as for the previous entry, phase 1's usage trim must bound its
  loop.
- **Upstream:** [#58136](https://tracker.ceph.com/issues/58136). Its fix,
  [ceph/ceph#49168](https://github.com/ceph/ceph/pull/49168), has been under
  review since 2022.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-006); verified 2026-09-27.

## radosgw faults on a zero rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards

- **Kind:** defect, unfixed through main.
- **Evidence:** `rgw_gc_max_objs`, `rgw_lc_max_objs` and `rgw_usage_max_shards`
  are plain `int`s with no `min:` and default 32 (v19.2.6 `rgw.yaml.in:1692`,
  `:427`, `:1515`), unlike the sibling `rgw_usage_max_user_shards`, which
  carries `min: 1` (`:1528-1540`). A configured 0 is accepted and then used as
  a divisor on the first request that shards:
  - GC: `RGWGC::tag_index` returns `rgw_shards_mod(hash, max_objs)`
    (`rgw_gc.cc:65`). At v19.2.2 `rgw_shards_mod` computes
    `hval % PRIME % max_shards` (`rgw_tools.h:45-52`), dividing by zero. From
    v19.2.3 (456a5e661d1) and v20.1.0 (a2b76b0e09e) it returns -1 for
    `max_shards <= 0`, so `tag_index` returns -1 and `send_chain` reads
    `obj_names[-1]` (`rgw_gc.cc:128-132`), out of bounds. GC enqueue runs on an
    overwrite or delete of a tailed object.
  - LC: `get_lc_index` computes `... % HASH_PRIME % max_objs` directly (v19.2.6
    `rgw_lc.cc:1974`), a divide-by-zero at every release that the
    `rgw_shards_mod` guard never covers; it runs from `guard_lc_modify`
    (`:2593`) on a bucket-lifecycle put (`:2636`) or delete (`:2668`).
  - Usage: `usage_log_hash` computes `val % max_shards` directly (v19.2.6
    `rgw_rados.cc:1624-1625`), a divide-by-zero at every release, from
    `log_usage` when usage logging is enabled and from usage read and trim.
  The same holds at v20.2.4 (`rgw_lc.cc:2031`, `rgw_rados.cc:1728-1729`, and
  GC's `obj_names[-1]` via `rgw_shards_mod` returning -1, `rgw_tools.h:63-71`).
  A negative value is accepted too. A negative `rgw_lc_max_objs` or
  `rgw_gc_max_objs` fails earlier, at startup: `RGWLC::initialize` and
  `RGWGC::initialize` allocate an array of that many shard names
  (`rgw_lc.cc:237-241` and `driver/rados/rgw_gc.cc:35-37` at v19.2.6 and
  v20.2.4), which with GCC throws a plain `std::bad_alloc`;
  `RGWRados::init_complete` runs both (`rgw_rados.cc:1326-1327` and, with
  GC, `:1223-1225` at v19.2.6; `:1310-1311` and `:1285-1287` at v20.2.4). A
  negative `rgw_usage_max_shards` does not fault, but `usage_log_hash`
  converts it to an unsigned divisor of 2^32 minus its magnitude, so each
  usage object is named by the hash itself (`rgw_rados.cc:1615-1628` at
  v19.2.6, `:1718-1731` at v20.2.4). The all-users usage read and trim walk
  the shard index up from 0 until the name comes back to `usage.0`, the read
  also stopping once it is truncated or has filled its entry budget
  (`:1681-1714` and `:1723-1733` at v19.2.6, `:1784-1817` and `:1826-1836`
  at v20.2.4). That divisor puts `usage.0` at least 2^31 steps away, each a
  RADOS call, and a missing shard object only moves the walk on: both skip
  ENOENT (`:1697-1698`, `:1729-1730`; v20.2.4 `:1800-1801`, `:1832-1833`),
  and the read clears `is_truncated` before its call
  (`cls/rgw/cls_rgw_client.cc:798-799` at v19.2.6, `:607-608` at v20.2.4).
  The admin API reaches both: a usage GET that names no user or bucket, and
  a usage DELETE with `remove-all`, read or trim for a user with an empty
  id (`rgw_rest_usage.cc:38-41`, `:69`, `:91-94` and `:108-120`, the same
  at both tags; `rgw_sal_rados.cc:252` and `:261` at v19.2.6, `:269` and
  `:278` at v20.2.4). This loop is from code reading, not reproduced.
- **Releases:** every release. GC faults as a SIGFPE at v19.2.2 and as an
  out-of-bounds read from v19.2.3 and v20.1.0 on; LC and usage fault as a
  SIGFPE throughout, since the `rgw_shards_mod` guard does not cover their
  direct modulo.
- **rgw-go:** config validation rgw-go must add. Phase 1 rejects or floors a
  zero `rgw_gc_max_objs` (GC worker, unit W), and a zero or negative
  `rgw_lc_max_objs` and `rgw_usage_max_shards` (metadata and lifecycle path,
  unit M), at startup rather than faulting on first use, and never divides
  by a shard count without guarding it.
- **Upstream:** [#80991](https://tracker.ceph.com/issues/80991), which we
  filed. Its fix, [ceph/ceph#72160](https://github.com/ceph/ceph/pull/72160),
  opened in response by the maintainer Matthew Heler (mheler) and not a
  draft, adds `min: 1` to the three options in `rgw.yaml.in` and is in
  review. It was verified to cover all three sites, and the option minimum
  also clamps a 0 already stored in the configuration when it is loaded.
  [#75958](https://tracker.ceph.com/issues/75958), a crash when
  rgw_gc_max_objs changes at runtime, is related but has a different trigger.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-019); verified 2026-09-27;
  not reproduced on a running cluster.

## radosgw truncates a long aws-chunked trailer section instead of rejecting it

- **Kind:** defect, unfixed through main, with a fix in review.
- **Evidence:** at v20.2.4 `AWSv4ComplMulti::complete` reads the trailer
  section into a 256-byte buffer but caps each read at 256 - pos - 1 bytes
  (`rgw_auth_s3.cc:1573-1576`; v19.2.6 `:1596-1599`). beast answers a
  0-byte read with 0 (`rgw_asio_frontend.cc:152-175`), so the position
  stops at 255, and the size check `tbuf_pos == trailer_buf_size` never
  fires (`rgw_auth_s3.cc:1585-1590`; v19.2.6 `:1608-1613`). Its error,
  ERR_LIMIT_EXCEEDED, is 409 LimitExceeded (`rgw_common.cc:87`). A longer
  section is silently cut at 255 bytes.
  - A signed trailer (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER`) whose
    `x-amz-trailer-signature` line is cut fails with 403
    SignatureDoesNotMatch (`rgw_auth_s3.cc:1663-1667`, v19.2.6
    `:1686-1690`; `rgw_common.cc:92`). Measured on v19.2.6 and v20.2.4:
    signed sections up to 257 bytes pass, since only the closing CRLF is
    lost, and longer ones fail with 403. A signed SHA512 trailer is 290
    bytes, so every signed SHA512 upload fails, on Squid too, which ignores
    checksums but still fails the request; v20.2.4 supports SHA512
    (`rgw_cksum.h:49`). A signed SHA256 trailer is 246 bytes and passes.
  - On v20.2.4 an unsigned section (`STREAMING-UNSIGNED-PAYLOAD-TRAILER`)
    whose checksum line falls past the cut is accepted, and the client's
    value is never compared: with no expected checksum, PutObj stores the
    one radosgw computed (`rgw_op.cc:4757-4790`).
  - Latent: the copy and the reads into the buffer write past the end of an
    empty `static_vector` (`rgw_auth_s3.cc:1569-1576`), which works only
    because its inline capacity is 256.
  - The same code is on main (`rgw_auth_s3.cc:1616` and `:1625`,
    2026-09-29).
- **Releases:** v19.1.0 and later, so every Squid and Tentacle release, and
  reef from v18.2.5; absent at v18.2.4. Checked at v18.2.5, v18.2.8,
  v19.1.0, v19.2.6, v20.2.4 and main.
- **rgw-go:** answers 409 LimitExceeded for a trailer section longer than
  1024 bytes, counted from the CRLF that ends the last data chunk
  (`internal/auth/chunked.go`, `finish`, `maxTrailerSection` = 1024), since
  the largest legitimate section, a signed SHA512 trailer, is 290 bytes.
  `docs/exclusions.md` records the difference from radosgw.
- **Upstream:** [#81122](https://tracker.ceph.com/issues/81122). The trailer
  code came with [ceph/ceph#54856](https://github.com/ceph/ceph/pull/54856),
  the fix for [#63153](https://tracker.ceph.com/issues/63153), and reached
  reef through [ceph/ceph#58435](https://github.com/ceph/ceph/pull/58435).
  [ceph/ceph#64934](https://github.com/ceph/ceph/pull/64934) rewrote the
  trailer parse but kept this read loop, and was closed unmerged.
  The fix in review, which we opened, is
  [ceph/ceph#72308](https://github.com/ceph/ceph/pull/72308) (draft). It
  grows the trailer buffer from 256 to 1024 bytes, enough for a signed
  SHA-512 trailer, and corrects the read cap, so a section that is still too
  long meets the size check and is refused rather than cut short.
- **Found:** phase 1 planning of unit A, 2026-09-29; reproduced 2026-09-29
  on disposable Squid and Tentacle clusters.

## radosgw accepts a negative or overflowing aws-chunked chunk size

- **Kind:** defect, unfixed through main, with a fix in review.
- **Evidence:** at v19.2.6 `AWSv4ComplMulti::ChunkMeta::create_next` reads
  each chunk size with `std::strtoull(metabuf, &data_field_end, 16)` and
  rejects only a parse that consumed nothing (`rgw_auth_s3.cc:1127-1132`);
  errno is never checked. strtoull negates a leading `-` in unsigned
  arithmetic and saturates on overflow, so "-1" and any size of more than
  16 significant hex digits become 2^64-1. The chunk's end,
  `offset + data_length`, then wraps (`:1095-1108`), and the rest of the
  current read is taken as this chunk's data, the next chunk's framing
  included. The same code is at v20.2.4 (`:1104-1109`, `:1072-1085`).
  `strtoull` also skips the CRLF that ends the previous chunk's data as
  leading whitespace, so a chunk whose data is not followed by that CRLF,
  or is followed by other blanks, frames correctly and the terminator is
  never validated; the code is identical at v19.2.6, v20.2.4 and main.
  - Measured on v19.2.6 and v20.2.4, with identical results. An unsigned
    upload (`STREAMING-UNSIGNED-PAYLOAD-TRAILER`) of two 48-byte chunks
    whose first size is "-1" or `1ffffffffffffffff` is answered 200 and
    stored corrupted: chunk one, then the six framing bytes between the chunks,
    then the first 42 bytes of chunk two. A single-chunk upload is not
    corrupted, since the object is capped at `x-amz-decoded-content-length`
    (`:1534-1542`).
  - The same upload, signed (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD`), fails
    with 400 XAmzContentSHA256Mismatch: the misframed bytes are hashed as
    one chunk, so the chunk-signature check that `complete()` makes for the
    last data chunk fails (`:1557-1563`), and
    `RGWOp::do_aws4_auth_completion` reports a failed `complete()` as that
    error (`rgw_op.cc:1370-1371`; v20.2.4 `:1607-1608`). A signed
    single-chunk upload with a malformed size is accepted with the exact
    bytes.
  - On main `create_next` no longer rejects even a parse that consumed
    nothing (`rgw_auth_s3.cc:1146-1151`, 2026-09-29).
  - The same call also reads past the bytes received. `parsing_buf`, a
    `boost::container::static_vector` of `META_MAX_SIZE` (101) bytes
    (`rgw_auth_s3.h:354` at v19.2.6, `:356` at v20.2.4), is not
    NUL-terminated. Each refill resizes it to its capacity, which zeroes
    the bytes the read does not fill (`rgw_auth_s3.cc:1295-1325` at
    v19.2.6, `:1272-1302` at v20.2.4), so a run of blanks and hex digits
    that ends inside the bytes received stops at a zero byte. Only a full
    buffer of blanks, an optional sign or `0x`, and hex digits running to
    its last byte takes strtoull past its 101 bytes. It reads on into the
    three padding bytes after them, which are indeterminate, and then the
    vector's size field, whose low byte is 101, an `e`, and whose next
    byte is zero, where it stops at the latest. That is the layout of the
    Boost 1.82 and 1.87 headers v19.2.6 and v20.2.4 build against, and a
    GCC 15 build measured it. The over-read cannot reach unmapped memory.
    A signed header in such a buffer has no `;` and is refused with 400
    (`rgw_auth_s3.cc:1140-1145` at v19.2.6, `:1117-1122` at v20.2.4). An
    unsigned header ends at the first CRLF that starts after the buffer's
    first byte (`:1190-1201`, `:1167-1178`), so such a buffer is accepted
    when its blanks hold one, as in `\r\n\r\n`, blanks, then the digits.
    The padding bytes enter `data_length` as its lowest digits for as long
    as each is a hex digit, followed by the `e` when all three are, and
    the chunk is framed with that length; with more than sixteen
    significant digits in all the size saturates instead, as above. The
    effect stays inside the requester's own upload: the length moves only
    where its own stream's chunks are framed, so in unsigned mode it can
    corrupt the object that request stores, as the lenient sizes above
    do, and the bytes read feed the length and are never stored or
    returned to the client. It is not security-relevant, and #72309's
    bounded `std::from_chars` parse removes the over-read entirely.
- **Releases:** every release since v12.1.0; checked at v19.2.6, v20.2.4 and
  main.
- **rgw-go:** answers 400 InvalidArgument for a chunk size that is not one
  to sixteen hex digits, before any of the chunk is stored
  (`internal/auth/chunked.go`, `parseChunkSize`), and for chunk data not
  followed by its CRLF (`nextHeader`). `docs/exclusions.md` records both
  differences from radosgw.
- **Upstream:** [#81123](https://tracker.ceph.com/issues/81123). The parse
  came with def8f6412a5, in
  [ceph/ceph#14885](https://github.com/ceph/ceph/pull/14885), and
  [ceph/ceph#63326](https://github.com/ceph/ceph/pull/63326) dropped its one
  check on main. [#21003](https://tracker.ceph.com/issues/21003) and
  [#45790](https://tracker.ceph.com/issues/45790) concern the same lenient
  chunk-metadata parse with other inputs; the latter's
  [ceph/ceph#35350](https://github.com/ceph/ceph/pull/35350) was closed
  unmerged.
  The fix in review, which we opened, is
  [ceph/ceph#72309](https://github.com/ceph/ceph/pull/72309) (draft). It
  parses the size strictly: one to sixteen hex digits, ending at `;` or the
  line's CRLF, and anything else is refused with 400 before any data is
  stored. On main, that restores the check #63326 dropped. It keeps the
  leading CRLF that ends the previous chunk's data optional, so it does not
  reject a chunk whose data is not followed by that CRLF; requiring it, as
  rgw-go does, is a design extension of this work, not a separate defect.
- **Found:** phase 1 planning of unit A, 2026-09-29; reproduced 2026-09-29
  on disposable Squid and Tentacle clusters.

## radosgw writes ACL owner and grantee names into its XML unescaped

- **Kind:** defect, not security-relevant, unfixed through main.
- **Evidence:** at v19.2.6 the S3 ACL writer puts the owner's ID and display
  name between their tags as they are (`rgw_acl_s3.cc:177`, `:179`), and a
  canonical-user grantee's the same way (`:260`, `:262`). GetBucketAcl and
  GetObjectAcl send that text as their document after the XML declaration
  (`dump_start`, `rgw_rest.cc:571-577`): `RGWGetACLs::execute` builds it with
  `rgw::s3::write_policy_xml` (`rgw_op.cc:5725-5734`), and
  `RGWGetACLs_ObjStore_S3::send_response` writes it with `dump_body`
  (`rgw_rest_s3.cc:3605-3614`), outside the XML formatter, which escapes
  the text of radosgw's other documents (`xml_stream_escaper`,
  `escape.cc:134-169`).
  - A display name holding markup therefore yields malformed XML, as `A&B`
    does, or well-formed XML that a client reads as another name or with
    extra markup, as `&amp;` (read as `&`) and `<b/>` (an empty `b`
    element) do; Python's ElementTree parses these documents so.
  - An administrator can set such a name: user create and modify, which
    radosgw-admin and the admin API run, copy the display name as given
    (`driver/rados/rgw_user.cc:1755`, `:2070`) and hold it to the IAM
    user-name pattern `[\w+=,.@-]+` only for an account's users (`:1840`,
    `:2198`; `rgw_rest_iam.cc:172-188`).
  - So can an OIDC identity provider. The first AssumeRoleWithWebIdentity
    for an unknown federated user creates its shadow user with the web
    token's `username` claim, or failing that `given_username`, as the
    display name (`rgw_auth.h:454-462`; `rgw_auth.cc:682` into
    `create_account`, `:611`), and stores it (`:618`).
  - The same at v20.2.4, where rgw_acl_s3.cc and escape.cc are
    byte-identical to v19.2.6 (`rgw_rest.cc:576-582`,
    `rgw_op.cc:6305-6314`, `rgw_rest_s3.cc:3888-3897`, `rgw_user.cc:1761`,
    `:2076`, `:1846`, `:2204`, `rgw_rest_iam.cc:175-191`, `rgw_auth.h:464-472`,
    `rgw_auth.cc:697`, `:626`, `:633`), and on main,
    which writes the same lines (2026-09-30).
- **Releases:** every release checked: v17.2.0, v18.2.8, v19.2.0, v19.2.6,
  v20.2.0, v20.2.4, v21.3.0 and main. The owner writer already wrote the ID
  and display name raw in 2009 (`rgw_acl.h:341-346` at f8fb331226c).
- **rgw-go:** the ACL documents (unit Z) escape the owner's and each
  grantee's ID and display name with `xmltext.Escape`, the escaping
  radosgw's formatter gives the text of its other documents (owner decision
  12c). `docs/exclusions.md` records the difference.
- **Upstream:** no tracker issue or pull request reports or fixes it
  (searched 2026-09-29). Each search was first run on a known match.
  - tracker.ceph.com, full text: every project's issues of every status
    through the issue filter "any searchable field contains", which covers
    subjects, descriptions and notes. It finds
    [#80948](https://tracker.ceph.com/issues/80948) by
    `oath_totp_validate4_callback`, a word only in its description, and
    [#73564](https://tracker.ceph.com/issues/73564) by a phrase only in a
    note; the site's search misses the first. Terms: `rgw_acl_s3`,
    `write_policy_xml`, `xml_stream_escaper`, `escape_xml`,
    `ACLOwner to_xml`, `ACLGrant to_xml`, `GetBucketAcl`, `GetObjectAcl`,
    `acl escape`, `acl escaping`, `DisplayName escape`,
    `display name escape`, `acl ampersand`, `acl xml invalid`,
    `acl xml malformed`, `acl not well-formed`, `unescaped xml`,
    `xml escape`, `xml escaping`, `ampersand`, `acl special characters`,
    `display name special characters`. Also the subjects of the 1,652 rgw
    issues updated since 2026-06-01.
  - Pull requests touching `src/rgw/rgw_acl_s3.cc` or its header. Merged:
    no change in the file's history on main since its creation in 2012
    escapes these fields (`git log -G escape` finds none), and squid and
    tentacle have not touched it since the floor tags. Open and closed
    unmerged: a scan of each pull request's changed files, which flags
    [ceph/ceph#54526](https://github.com/ceph/ceph/pull/54526) among
    November 2023's merged rgw pull requests, finds the file in 2 of the
    1,562 open pull requests, which change only an include, and in 38
    of the 2,156 closed unmerged pull requests labelled rgw (the labeler
    adds that label to every pull request touching `src/rgw/` since
    2020-11-12). None of those changes escapes these fields; ten that
    touch over 2,000 files, branches merged onto the wrong base, were
    not read.
  - Pull request text (titles, bodies and comments), which finds
    [ceph/ceph#54526](https://github.com/ceph/ceph/pull/54526) by a phrase
    only in its body: `acl escape`, `acl xml escape`, `DisplayName escape`,
    `display name xml`, `rgw_acl_s3 escape`, `to_xml escape`,
    `xml_stream_escaper`, `acl ampersand`, `GetBucketAcl xml`,
    `acl invalid xml`. The one hit,
    [ceph/ceph#19845](https://github.com/ceph/ceph/pull/19845), reworked the
    formatter's escaping and does not touch the ACL writer.
  - Related: [#59077](https://tracker.ceph.com/issues/59077), open since
    2023-03, proposes refusing user IDs and display names that hold REST or
    IAM-policy delimiters; it does not mention the ACL documents.
  - Not filed: the defect has not been reproduced on a running cluster.
- **Found:** phase 1 planning of unit Z, 2026-09-29; derived from the
  source, not reproduced.

## radosgw ignores a payload-hash mismatch on bodies read by read_all_input

- **Kind:** defect, a regression; unfixed through main, with a fix in review.
- **Evidence:**
  - A signed single-chunk body gets an `AWSv4ComplSingle` completer (v19.2.6
    `rgw_rest_s3.cc:5903-5946`; v20.2.4 `:6470-6517`), whose `complete()`
    compares the body's SHA-256 with `x-amz-content-sha256`
    (`rgw_auth_s3.cc:1737-1756`; v20.2.4 `:1714-1733`).
  - `RGWOp::read_all_input` and `get_json_input` read the body, call
    `do_aws4_auth_completion()` and drop its result (v19.2.6
    `rgw_op.h:215-237`; v20.2.4 `:229-251`). `do_aws4_auth_completion` moves
    the completer out of the request before it checks (v19.2.6
    `rgw_op.cc:1367`; v20.2.4 `:1604`), so any later call returns 0.
  - CreateBucket, PutBucketAcl and PutObjectAcl, PutBucketPolicy,
    PutObjectTagging and PutBucketTagging, CompleteMultipartUpload and
    DeleteObjects read their body through `read_all_input` (v19.2.6
    `rgw_rest_s3.cc:2491`, `rgw_rest.cc:1474`, `rgw_op.cc:8078`,
    `rgw_rest_s3.cc:788`, `:880`, `rgw_rest.cc:1590`, `:1672`; v20.2.4
    `rgw_rest_s3.cc:2611`, `rgw_rest.cc:1479`, `rgw_op.cc:9004`,
    `rgw_rest_s3.cc:870`, `:962`, `rgw_rest.cc:1595`, `:1677`). The checks
    that the ACL PUTs, CompleteMultipartUpload and DeleteObjects make
    afterwards therefore always pass (v19.2.6 `rgw_rest_s3.cc:3618-3623`,
    `:4073`, `:4244`; v20.2.4 `:3901-3906`, `:4601`, `:4792`), and none of
    these ops compares a Content-MD5 instead. radosgw acts on a signed body
    whose hash does not match and answers as if it did; the mismatch is only
    logged, at level 10.
  - The other subresource PUTs read their body the same way: lifecycle,
    object lock, legal hold, replication, versioning, website, CORS, request
    payment, retention, public-access block and encryption (v19.2.6
    `rgw_rest.cc:1482-1496`, `rgw_op.cc:8616`, `:8747`, and six sites in
    `rgw_rest_s3.cc`). Of them only the lifecycle PUT compares a Content-MD5,
    when one is sent (`rgw_op.cc:5942-5981`).
  - PutObject and UploadPart do return the verdict (v19.2.6
    `rgw_rest_s3.cc:2706-2714`, `rgw_op.cc:4439`; v20.2.4
    `rgw_rest_s3.cc:2867-2875`, `rgw_op.cc:4671`).
  - It is a regression. 0214b4a7afb, "rgw: handle aws4 completion when
    reading all op data" (first in v17.1.0), moved these ops to the helper.
    Before it, CreateBucket, the ACL PUTs, CompleteMultipartUpload,
    DeleteObjects and the versioning, website and CORS PUTs returned the
    verdict (v16.2.15 `rgw_rest_s3.cc:2268-2271`, `:3341`, `:3721`, `:3903`,
    `:1978`, `:2052`, `:3486`).
  - The same code is on main (`rgw_op.h:237` and `:248`, 7ed73efc1be,
    2026-09-25).
- **Trigger:** a signed request whose body changes after signing. The
  signature covers the `x-amz-content-sha256` value, not the body, so anyone
  who can alter a request between the client and radosgw (plain HTTP, or
  behind a proxy that terminates TLS) can replace such a body, for example a
  bucket policy, an ACL, a DeleteObjects key list or a CompleteMultipartUpload
  part list, and radosgw acts on the replacement.
- **Releases:** every release from v17.1.0 on; checked at v16.2.15, which
  returns the verdict, and at v19.2.6, v20.2.4 and main.
- **rgw-go:** phase 1 verifies the hash before it acts: every op that acts on
  a request body reads the body to its end first, and a mismatch is 400
  XAmzContentSHA256Mismatch with nothing changed (units M, W and P). That is
  a difference from radosgw, which those units record in `docs/exclusions.md`.
- **Upstream:** we filed [#81230](https://tracker.ceph.com/issues/81230).
  The fix in review is
  [ceph/ceph#72263](https://github.com/ceph/ceph/pull/72263) (draft): both
  helpers return the completion's result, a two-line `rgw_op.h` change that
  covers every affected op. It has not been compiled against a patched build
  yet; the pull request says so, and Ceph's CI builds it. No earlier tracker
  issue or pull request reports it (full-text tracker and all-time
  pull-request search, 2026-09-30).
  [#71607](https://tracker.ceph.com/issues/71607) and
  [ceph/ceph#64569](https://github.com/ceph/ceph/pull/64569) are related but
  distinct: they changed only the single-object DeleteObj `get_params` path.
  The helper came with
  [ceph/ceph#39678](https://github.com/ceph/ceph/pull/39678).
- **Found:** phase 1 planning of unit A, 2026-09-29; derived from the source
  and verified 2026-09-30. Reproduced live on v19.2.6 and v20.2.4 on
  2026-09-30. PutBucketTagging, PutBucketPolicy and CreateBucket accept a
  substituted body, and the readback shows the substitute applied, while
  PutObject answers 400. With `debug_rgw=20` the completer logs the mismatch
  while the op answers 2xx. The reproducer, its output and the debug excerpt
  are attached to the tracker issue.

## radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose Content-Type is unsigned

- **Kind:** defect, a regression; fixed upstream, but in no squid or tentacle
  release yet.
- **Evidence:**
  - Unless `rgw_sigv4_insecure` is set, `get_v4_canonical_headers` refuses a
    request that carries a Content-Type header its SignedHeaders do not list,
    logging "Signature rejected: 'content-type' supplied but not in
    CanonicalHeaders" (`rgw_auth_s3.cc:788-802` at v19.2.6, `:765-779` at
    v20.2.4). The same block requires `host` and every `x-amz-*` header to be
    signed. So a presigned PUT URL signed over `host` alone fails once the
    client that uses it sends a Content-Type.
  - The check came with the CVE-2026-54330 hardening, "rgw: Follow
    guidelines when verifying SigV4 signatures" (d8ba9d0cb06 on squid and
    683fad73231 on tentacle, both 2026-06-16); v19.2.5 and v20.2.3 do not
    have it.
  - The fix, 534308306f2, removes the Content-Type requirement and keeps the
    `host` and `x-amz-*` checks.
- **Releases:** v19.2.6 and v20.2.4. The fix is on main (c3179de982f,
  2026-08-21), the squid branch (18d35e32770, 2026-08-26) and the tentacle
  branch (7e2e2072822, 2026-09-03), but neither branch has had a release
  since (checked 2026-09-30). The first tag that carries it is v21.1.1
  (2026-09-10), an umbrella release candidate; no umbrella tag ever carried
  the check.
- **rgw-go:** reproduces it, as both floor releases carry it.
  `canonicalHeadersV4` (`internal/auth/sigv4.go`) answers AccessDenied to a
  request with a Content-Type its SignedHeaders do not list, as it does to
  an unsigned `host` or `x-amz-*` header, unless `rgw_sigv4_insecure`
  (`auth.Config.Insecure`) is set. The vector
  `internal/auth/testdata/v4/unsigned-content-type-rejected.json` pins it.
  Because rgw-go behaves as radosgw does, `docs/exclusions.md` has no entry
  for it.
- **Upstream:** [#79674](https://tracker.ceph.com/issues/79674), resolved,
  with its duplicate [#79708](https://tracker.ceph.com/issues/79708) and its
  backports [#79723](https://tracker.ceph.com/issues/79723) (squid),
  [#79725](https://tracker.ceph.com/issues/79725) (tentacle) and
  [#79724](https://tracker.ceph.com/issues/79724) (umbrella). The fix is
  [ceph/ceph#71192](https://github.com/ceph/ceph/pull/71192), backported by
  [ceph/ceph#71296](https://github.com/ceph/ceph/pull/71296) (squid),
  [ceph/ceph#71364](https://github.com/ceph/ceph/pull/71364) (tentacle) and
  [ceph/ceph#71363](https://github.com/ceph/ceph/pull/71363) (umbrella).
- **Found:** phase 1 authentication work (unit A), 2026-09-30, in the
  prior-art search for url_decode; verified in the source at both tags. Not
  reproduced by rgw-go; the reporter of #79674 met it on a v20.2.4 cluster.

## radosgw's SigV4 signing key is undefined for a secret byte above 0x7f

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/`. The code is the same at v19.2.6,
  v20.2.4 and main (06adccc25d6, 2026-10-01); lines are given per tag.
  - `get_v4_signing_key` keys its first HMAC with `transform_secret_key`,
    which appends to "AWS4", for each `char` of the secret, the `n` bytes
    `encode_utf8(c, buf)` reports writing into a six-byte buffer
    (`rgw/rgw_auth_s3.cc:984-1004` at v19.2.6, `:961-981` at v20.2.4,
    `:1002-1022` on main).
  - Ceph compiles with `-fsigned-char` (`CMakeLists.txt:92-95` at
    v19.2.6, `:99-102` at v20.2.4 and main), so a byte above 0x7f reaches
    `encode_utf8(unsigned long, unsigned char *)` sign-extended, above
    0x7fffffff, and it returns -1 (`common/utf8.c:62-102` at both tags,
    `:63-103` on main). `n` is a `size_t`, so it becomes SIZE_MAX, and
    `std::begin(buf) + n` points outside the buffer: the behaviour is
    undefined.
  - Were the byte read as the code point the function intends, it would
    still become two bytes, so the key would differ from the one an AWS
    client derives from the secret's bytes.
  - radosgw accepts any non-empty secret an administrator supplies
    (`rgw/driver/rados/rgw_user.cc:587-597` at v19.2.6, `:592-602` at
    v20.2.4). Only the secrets it generates are known to be ASCII:
    `rgw_generate_secret_key` makes them alphanumeric (`:500-506`,
    `:505-511`).
- **Impact:** radosgw derives the signing key from the stored secret before
  it compares signatures (`rgw/rgw_rest_s3.cc:6362-6364` at v19.2.6,
  `:6933-6935` at v20.2.4). So any SigV4 request that names the access key
  ID of a secret holding a byte above 0x7f reaches the undefined behaviour,
  whoever sends it and whatever its signature.
  - libstdc++'s range insert sees a distance of SIZE_MAX and throws
    `std::length_error`, which `Strategy::apply` catches as a
    `std::exception` and turns into -EPERM, a 403 (`rgw/rgw_auth.cc:490-555`
    at v19.2.6, `:505-570` at v20.2.4). A probe of the same insert, built
    with g++ 11.5, 13.5, 14.4 and 15.3, throws it both plainly and with
    `-D_GLIBCXX_ASSERTIONS`, which the shipped el9 packages likely carry
    through `RPM_OPT_FLAGS` (`ceph.spec.in:1360-1361`; Ceph's own CMake
    adds it only to a Debug build, `src/CMakeLists.txt:190-193`). Only
    `-D_GLIBCXX_DEBUG`, which no shipped build uses, aborts instead. Not
    reproduced against a running radosgw.
  - So the only effect is that the user holding such a secret can never
    authenticate. It is not security-relevant: nothing crashes, and the
    throw yields no key, so no other secret can sign as this one.
- **Releases:** v19.2.6, v20.2.4 and main 06adccc25d6; older releases not
  checked.
- **rgw-go:** derives the signing key from the secret's own bytes
  (`signingKeyV4`, `internal/auth/sigv4.go`), as AWS clients do, so such a
  secret signs and verifies. `docs/exclusions.md` records the difference
  ("A secret key signs with its own bytes").
- **Upstream:** none for this site. A prior-art search on 2026-10-02 found
  no issue or fix PR; it is unfiled while filing is paused. It is the same
  `-fsigned-char` high-byte class as [url_decode reads outside its hex table
  for a byte above 0x7f after
  "%"](#url_decode-reads-outside-its-hex-table-for-a-byte-above-0x7f-after-),
  which we filed as [#81267](https://tracker.ceph.com/issues/81267) with a
  fix in review, [ceph/ceph#72272](https://github.com/ceph/ceph/pull/72272);
  a report of this site cross-references them.
- **Found:** phase 1 unit A, Task 3, 2026-10-01, transcribing
  `get_v4_signing_key`; derived from the source, not reproduced.

## radosgw checks a copy source with inputs from the destination bucket

- **Kind:** defect, unfixed through main, with a fix in review for the owner
  and tenant inputs.
- **Evidence:** CopyObject and UploadPartCopy authorize their source inside
  `verify_permission` through the request's `req_state`, whose bucket is the
  destination (v19.2.6 `rgw_op.cc:5420-5422` and `:5454-5459`, `:3933-3934`
  and `:3956-3961`; v20.2.4 `:5986-5988`, `:6020-6025`, `:4142-4143` and
  `:4165-4170`). The source's ACLs and stored policy are read, but three
  inputs are the destination's:
  - Owner: for an identity that belongs to an account, the cross-account
    decision compares the account with `s->bucket_owner` (v19.2.6
    `rgw_common.cc:1386-1403`; v20.2.4 `:1414-1433`), the owner of the
    destination's ACL (`rgw_op.cc:552`; v20.2.4 `:582`). An account user
    copying from another account's bucket into their own account's bucket
    takes the same-account path, which consults no ACL and allows on an
    Allow from an identity policy, or for the account root (v19.2.6
    `rgw_common.cc:1236-1244`; v20.2.4 `:1249-1257`). The source's owner
    need grant nothing, where a GET of the same object needs its grant.
    CopyObject decides on the bucket owner alone. UploadPartCopy decides on
    the owner of the source object's ACL and falls back to `s->bucket_owner`
    (v19.2.6 `rgw_common.cc:1533-1534`; v20.2.4 `:1585-1586`), and a source
    object with no ACL attr gets a default ACL owned by `s->bucket_owner`
    (v19.2.6 `rgw_op.cc:421-422`, `:307-310`; v20.2.4 `:451-452`,
    `:350-353`), so UploadPartCopy is affected only for a source object
    whose ACL is missing or names no owner.
  - Tenant: the source bucket's policy is parsed with `s->bucket_tenant`,
    the destination's tenant (v19.2.6 `rgw_op.cc:419`; v20.2.4 `:449`). The
    parser binds a Resource whose account is empty or `*` to that tenant
    (`rgw_iam_policy.cc:690-696`; v20.2.4 `:703-709`), while the source
    object's ARN carries the source's tenant, which a match compares
    (`rgw_arn.cc:126-130` and `:335-337` at both tags). In a cross-tenant
    copy the policy's Resource elements never match the source object and
    its NotResource elements never exclude it, so a Deny the owner scoped
    with Resource does not stop a copy that the source's ACL allows. A
    Resource that names the source's tenant fails the parse
    (`rgw_iam_policy.cc:697-701`; v20.2.4 `:710-714`); see the next entry.
  - Public-access block: the permission state takes the bucket and its
    public-access block from the request (v19.2.6 `rgw_common.cc:1099-1109`,
    `rgw_op.cc:585`; v20.2.4 `rgw_common.cc:1112-1122`, `rgw_op.cc:615`).
    The source's ACLs are evaluated with the destination's IgnorePublicAcls
    (v19.2.6 `rgw_common.cc:1420-1423`, `:1572-1575`; v20.2.4 `:1453-1454`,
    `:1628-1629`), and v20.2.4 tests RestrictPublicBuckets with the
    destination's block and owner against the source's policy (`:1376-1377`,
    `:1543-1544`); v19.2.6 does not evaluate RestrictPublicBuckets. A public
    ACL grant, or on v20.2.4 a public policy, on a source whose owner
    blocked it lets a requester copy the object into a bucket without the
    block, where a GET is refused.
  - v20.2.4's source check also compares `x-amz-expected-bucket-owner` with
    the destination's owner (`rgw_common.cc:1407-1412`, `:1575-1580`). That
    header names the destination bucket, so this comparison is correct.
  - The owner and tenant inputs are the same on main (`rgw_common.cc:1430`,
    `:1607`, `rgw_op.cc:524-526`, 7ed73efc1be, 2026-09-25).
- **Trigger:** a requester who may write to the destination: an account user
  whose account owns it, for the owner input; a requester whose destination
  is in another tenant than the source, for the tenant input; and a
  destination without the source's public-access block, for the last.
- **Releases:** every Squid and Tentacle release; checked at v19.2.6, v20.2.4
  and main. The owner input came with IAM accounts in f917e999c2c, "rgw: add
  cross-account policy evaluation" (first in v19.1.0), and the tenant input
  in v19.2.0 and v20.1.0 (see Upstream); reef parsed the source's policy
  with the source's tenant (v18.2.8 `rgw_op.cc:403`).
- **rgw-go:** phase 1 (unit Z) checks a copy source against the source
  bucket: its owner decides the cross-account path, is the object-owner
  fallback and owns a default object ACL; its policy is parsed with its own
  tenant; and its public-access block applies in addition to the
  destination's. The destination supplies only requester pays and the
  `x-amz-expected-bucket-owner` comparison. That is a difference from
  radosgw, which unit Z records in `docs/exclusions.md`.
- **Upstream:** we filed [#81248](https://tracker.ceph.com/issues/81248).
  The fix in review is
  [ceph/ceph#72270](https://github.com/ceph/ceph/pull/72270) (draft): the
  source check takes the source bucket's tenant and owner, through a new
  `verify_bucket_permission` overload that leaves the other callers
  unchanged. It does not change the public-access block input: the source
  check still applies the destination's IgnorePublicAcls and
  RestrictPublicBuckets, which matters only for a public source, and the
  issue and the pull request leave that as a follow-up. The pull request
  has not been compiled against a patched build yet, and says so. No
  earlier tracker issue or pull request reports it (full-text tracker and
  all-time pull-request search, 2026-09-30). The tenant input came with
  [ceph/ceph#59169](https://github.com/ceph/ceph/pull/59169) and its squid
  backport [ceph/ceph#59221](https://github.com/ceph/ceph/pull/59221), the
  fix for [#67464](https://tracker.ceph.com/issues/67464), which pass the
  request's tenant where reef passed the source's.
  [#61954](https://tracker.ceph.com/issues/61954) concerns the same
  UploadPartCopy check, for a source policy that was never read.
- **Found:** phase 1 planning of unit Z, 2026-09-29; derived from the source
  and verified 2026-09-30. Reproduced live on v19.2.6 and v20.2.4 on
  2026-09-30, cross-tenant with IAM accounts: tenant B's GET of tenant A's
  private object answers 403, while B's CopyObject of the same object into
  B's own bucket answers 200, and the copy reads back byte-identical. The
  radosgw log shows the account-root grant deciding `s3:copy_obj`. The
  reproducer, written in Go, its output and the patch are attached to the
  tracker issue.

## radosgw terminates on a copy source or system request whose bucket policy does not parse

- **Kind:** defect, security-relevant: a low-privilege, shared-fate denial
  of service under version skew (triage estimate CVSS 5.3-7.5), unfixed
  through main, with a fix in review. Unreproduced as a version-skew
  attack; the cross-tenant trigger was reproduced live (Found, below).
- **Evidence:**
  - CopyObject and UploadPartCopy parse the source bucket's stored policy
    first thing in `verify_permission`, with no handler (v19.2.6
    `rgw_op.cc:5420` and `:3933` into `read_obj_policy`, `:419`; v20.2.4
    `:5986`, `:4142`, `:449`). The parse constructs a `Policy` (v19.2.6
    `rgw_op.cc:328-338`; v20.2.4 `rgw_common.cc:3275-3285`), which throws
    `PolicyParseException` when it fails (`rgw_iam_policy.cc:1815-1824`;
    v20.2.4 `:1850-1859`). The same parse of the request's own bucket
    policy has a handler, which answers 403 (v19.2.6 `rgw_op.cc:598-615`;
    v20.2.4 `:628-645`).
  - Nothing on the request path catches the exception. `process_request`
    catches only `DigestException` (v19.2.6 `rgw_process.cc:343` and
    `:410`; v20.2.4 `:345` and `:417`), and the completion handler of the
    beast connection coroutine rethrows it (v19.2.6
    `rgw_asio_frontend.cc:1203-1205`, `:1220-1222`; v20.2.4 `:1116-1117`,
    `:1133-1134`). The frontend runs on radosgw's io_context pool (v19.2.6
    `rgw_appmain.cc:474`; v20.2.4 `:482`), whose threads call `ioctx.run()`
    with no handler either (v19.2.6 `common/async/context_pool.h:81-85`;
    v20.2.4 `:80-84`; `common/Thread.h:72-82` at both tags), so the
    exception leaves the thread and `std::terminate` ends the process.
  - A stored policy fails the parse when the parser has changed since it was
    stored, which the handler for the request's own bucket anticipates ("a
    parsing failure here means we broke backward compatibility", v19.2.6
    `rgw_op.cc:603-606`). From v19.2.0 a policy that parses for its own
    bucket fails too: the source's policy is parsed with the destination's
    tenant (previous entry), and a Resource naming the source's tenant,
    which the source's PutBucketPolicy accepts (v19.2.6
    `rgw_op.cc:8099-8101`; v20.2.4 `:9025-9027`), fails for any other
    tenant (`rgw_iam_policy.cc:697-701`; v20.2.4 `:710-714`).
  - A system request, one authenticated as a system user as multisite sync
    is (`rgw_auth_filters.h:320-330`; v20.2.4 `:348-356`), meets the same
    unhandled parse on its own bucket. The handler in `init_permissions`
    refuses only other requests (`rgw_op.cc:612-614`; v20.2.4 `:642-644`),
    so a system request goes on to `read_permissions`, whose
    `rgw_build_object_policies` calls `read_obj_policy` on the same bucket
    (`rgw_op.cc:8003-8009`, `:646-648`; v20.2.4 `:8921-8927`, `:676-678`),
    and that parses the policy again with no handler (`:419`; v20.2.4
    `:449`). At v20.2.4 the handler exempts every request for which
    `is_admin()` holds, an admin user's as well as a system user's
    (`rgw_auth.cc:1068-1071`), so an admin user's object request takes the
    same path there.
  - CopyObject's `verify_permission` parses the destination bucket's policy
    once more, also with no handler (v19.2.6 `rgw_op.cc:5475`; v20.2.4
    `:6041`; main `:6394` at 7ed73efc1be). `init_permissions` has already
    refused any other request whose own bucket policy does not parse, so
    only a system request, and at v20.2.4 an admin user's, reaches this
    parse with such a policy.
  - The same code is on main (`rgw_op.cc:524`, `rgw_process.cc:461`,
    `rgw_asio_frontend.cc:1204` and `:1221`, 7ed73efc1be, 2026-09-25).
- **Trigger:** a CopyObject or UploadPartCopy that names such a source and
  reaches `verify_permission`. The parse comes before the copy's permission
  checks, so the requester needs no grant on either bucket; for a source
  whose policy names its own tenant, any such request whose destination
  bucket is in another tenant ends the process. Also an object request by
  a system user, or at v20.2.4 by an admin user, on a bucket whose own
  stored policy does not parse.
  - Version skew gives a low-privilege trigger. A policy that names an
    action only Tentacle knows, such as `s3:GetObjectAttributes` or
    `s3:ReplicateObject` (v20.2.4 `rgw_iam_policy.cc:97`, `:142`; neither
    is in v19.2.6's table), is stored by a Tentacle gateway and fails a
    Squid gateway's parse: the unknown action fails `do_string`
    (v19.2.6 `rgw_iam_policy.cc:589`, `:753-757`) and `Policy::Policy`
    throws (`:1823-1824`). Squid's PutBucketPolicy refuses such a policy
    with EINVAL (v19.2.6 `rgw_op.cc:8099-8101`, `:8116-8118`), so storing
    it needs the newer writer and reading it the older one: a single-zone
    rolling upgrade or downgrade over shared pools, or a mixed-version
    multisite secondary, which no cluster has verified. Any authenticated
    user that names that bucket as a copy source then ends the Squid
    gateway, since the parse in `verify_permission` (`rgw_op.cc:5420`)
    runs before the permission check (`:5454-5459`).
- **Releases:** every release checked, v19.2.6, v20.2.4 and main; the
  cross-tenant case from v19.2.0 and v20.1.0.
- **rgw-go:** phase 1 (unit Z) refuses a copy whose source bucket policy does
  not parse with 403 AccessDenied, for every identity with no admin bypass,
  and parses the source's policy with the source's own tenant, so a policy
  that parses for its own bucket parses for a copy too. For the request's
  own bucket it refuses such a policy with 403 unless the requester is an
  admin, whose request is then evaluated without the policy, an object
  request included. DeleteObjects checks each key as it checks a copy
  source and parses the policy again, so an admin's DeleteObjects on such a
  bucket is refused for every key. That is a difference from radosgw,
  which unit Z records in `docs/exclusions.md`.
- **Upstream:** we filed [#81253](https://tracker.ceph.com/issues/81253).
  The fix in review is
  [ceph/ceph#72271](https://github.com/ceph/ceph/pull/72271) (draft): it
  wraps both copy-path parses, the source's in `read_obj_policy` and the
  destination's in CopyObject's `verify_permission`, in the handler the
  request's own bucket already has. An ordinary request is refused with
  403, and an admin or system request continues so the bucket can be
  repaired. It touches the same `read_obj_policy` statement as
  [ceph/ceph#72270](https://github.com/ceph/ceph/pull/72270) (previous
  entry), so whichever merges second rebases. The pull request has not been
  compiled against a patched build yet, and says so. No earlier tracker
  issue or pull request reports it (full-text tracker and all-time
  pull-request search, 2026-09-30). The cross-tenant case came with
  [ceph/ceph#59169](https://github.com/ceph/ceph/pull/59169) and
  [ceph/ceph#59221](https://github.com/ceph/ceph/pull/59221) (previous
  entry). The version-skew trigger was classified after filing, on
  2026-10-05.
- **Found:** phase 1 planning of unit Z, 2026-09-29; derived from the source
  and verified 2026-09-30. Reproduced live on a stock v19.2.6 on
  2026-09-30. A cross-tenant CopyObject, whose source policy names its own
  tenant in a Resource, aborted radosgw with SIGABRT (exit 134), and its
  container restarted each time. The log reaches `s3:copy_obj verifying op
  permissions` and ends in `std::terminate`, through the asio coroutine's
  rethrow, before any permission decision. `ceph crash ls` stays empty,
  because that path posts nothing to the crash module. The steps and the
  log are attached to the tracker issue. Not run live: v20.2.4, and the
  system-request path.

## radosgw checks a CopyObject source against its bucket's ACL, not the object's

- **Kind:** defect, security-relevant: an authorization bypass that lets a
  bucket READ grantee copy an object whose own ACL refuses them (triage
  estimate CVSS 6.5-7.7); a regression, unfixed through main. It is
  distinct from [#81248](https://tracker.ceph.com/issues/81248), the
  copy-source check with the destination's inputs, and survives its fix,
  [ceph/ceph#72270](https://github.com/ceph/ceph/pull/72270).
- **Evidence:**
  - CopyObject reads the source object's ACL into `src_acl` and never uses
    it (v19.2.6 `rgw_op.cc:5409`, `:5420-5422`; v20.2.4 `:5975`,
    `:5986-5988`). It authorizes `s3:GetObject` with
    `verify_bucket_permission`, passing the source bucket's ACL
    (`:5437-5459`; v20.2.4 `:6003-6025`), and when no policy decides, that
    ACL is checked for READ, the permission `s3:GetObject` maps to
    (`rgw_common.cc:1412-1432`, `rgw_iam_policy.h:237-252`; v20.2.4
    `rgw_common.cc:1442-1469`, `rgw_iam_policy.h:247-265`).
  - So a READ grant on the source bucket, which for S3 means listing it,
    lets its grantee copy any object in it into a bucket they may write,
    including an object whose own ACL grants them nothing and whose GET is
    refused; and a READ grant on an object alone no longer lets its grantee
    copy it. UploadPartCopy still checks the object's ACL
    (`verify_object_permission` with `cs_acl`, `rgw_op.cc:3956-3961`;
    v20.2.4 `:4165-4170`).
  - It is a regression. At v18.2.8 CopyObject checked `src_acl` for READ
    (`rgw_op.cc:5415-5428`). 897d4063871, "rgw/auth: object ops use new
    verify_bucket_permission() overload" (first in v19.1.0), replaced that
    check with the bucket form.
  - The same code is on main (`rgw_op.cc:6328`, `:6340` and `:6374`,
    7ed73efc1be, 2026-09-25).
- **Releases:** every Squid and Tentacle release; checked at v18.2.8, which
  checks the object's ACL, and at v19.2.6, v20.2.4 and main.
- **rgw-go:** requires READ on both ACLs (owner ruling, option C): when no
  bucket or identity policy decides, W's CopyObject checks its source with
  `VerifyBucketPermissionIn` against the bucket's ACL and then with
  `VerifyObjectPermissionIn` against the object's
  (`internal/op/copyobject.go`). It so refuses the copies the regression
  lets a bucket grantee make, allows none radosgw refuses, and refuses an
  object-only grantee as radosgw does. `docs/exclusions.md` records the
  difference ("A copy source needs READ in its bucket's ACL and in its
  own"). If upstream returns to the object's ACL, rgw-go may relax to the
  object's alone.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. No tracker issue or pull request reports it
  (full-text tracker and all-time pull-request search, 2026-09-30). The
  bucket form reached squid through
  [ceph/ceph#56863](https://github.com/ceph/ceph/pull/56863). Not filed: the
  defect has not been reproduced on a running cluster, and filing needs a
  live reproduction and a C++ reproducer.
- **Found:** reading the copy-source checks for the previous entries,
  2026-09-30; derived from the source, not reproduced.

## Squid accepts RestrictPublicBuckets but never enforces it

- **Kind:** defect, fixed in Tentacle.
- **Evidence:**
  - A bucket's public-access block carries RestrictPublicBuckets
    (`rgw_public_access.h:41`), but at v19.2.6 nothing in `src/rgw` reads it
    except the block's printer (`rgw_public_access.cc:32`). A bucket whose
    owner sets it keeps granting its public bucket policy to every
    requester.
  - 07ad231606d, "rgw: implement RestrictPublicBuckets from
    PublicAccessBlock" (first in v20.1.0), makes the bucket and object checks
    refuse a requester outside the bucket owner's account when the bucket
    policy is public (v20.2.4 `rgw_common.cc:1374-1380`, `:1541-1547`).
- **Releases:** every release before v20.1.0, checked at v19.2.6; fixed from
  v20.1.0, checked at v20.2.4.
- **rgw-go:** phase 1 (unit Z) enforces it on both releases, as v20.2.4's
  radosgw does. That is a difference from Squid's radosgw, which unit Z
  records in `docs/exclusions.md`.
- **Upstream:** [#65741](https://tracker.ceph.com/issues/65741), fixed by
  [ceph/ceph#57206](https://github.com/ceph/ceph/pull/57206). Its squid
  backport, [#70860](https://tracker.ceph.com/issues/70860), is New with no
  pull request, and the reef one,
  [#70859](https://tracker.ceph.com/issues/70859), was rejected (tracker and
  pull-request search, 2026-09-30).
- **Found:** reading the copy-source checks for the copy-source entries
  above, 2026-09-30; derived from the source.

## radosgw answers 200 to a CreateBucket that loses a race to another owner

- **Kind:** defect, a regression; unfixed through main, with a fix in review
  upstream.
- **Evidence:**
  - `RGWCreateBucket::execute` reads the bucket first (v19.2.6
    `rgw_op.cc:3541-3546`; v20.2.4 `:3769-3774`). A bucket another owner
    already holds is refused there with 409 BucketAlreadyExists, since its
    ACL, owner included, differs from the request's (v19.2.6
    `rgw_op.cc:3569-3578`, `rgw_acl.cc:62-64`, `rgw_common.cc:113`; v20.2.4
    `rgw_op.cc:3797-3806`, `rgw_common.cc:114`).
  - When the name is free at that read and another owner's create lands
    before this request's own (`rgw_op.cc:3640`; v20.2.4 `:3868`),
    `RGWRados::create_bucket` returns EEXIST with the existing bucket's info
    (`rgw_rados.cc:2422-2450`; v20.2.4 `:2530-2558`), and
    `RadosBucket::create` returns `-ERR_BUCKET_EXISTS` for another owner, as
    it does for the same owner (`rgw_sal_rados.cc:182-183`; v20.2.4
    `:192-193`). `execute` continues past that code (`rgw_op.cc:3646-3647`;
    v20.2.4 `:3874-3875`). Its ownership re-check runs only for a bucket
    that existed at the read, and only when metadata is uploaded, which S3's
    CreateBucket never does (`rgw_op.cc:3649-3665`, `rgw_op.h:1121`; v20.2.4
    `rgw_op.cc:3877-3893`, `rgw_op.h:1189`). `send_response` turns
    `-ERR_BUCKET_EXISTS` into 200 (`rgw_rest_s3.cc:2544-2547`; v20.2.4
    `:2705-2708`).
  - The client is told its create succeeded, for a bucket that another owner
    holds and that is not linked to the client. What it can then do in that
    bucket is what the other owner's ACL and policy allow.
  - It is a regression. At v18.2.8 `RadosUser::create_bucket` returned EEXIST
    for another owner (`rgw_sal_rados.cc:276-277`), which CreateBucket
    answered with 409 (`rgw_rest_s3.cc:2545-2548`, `rgw_common.cc:109`).
    e2eb66a3617, "rgw/sal: move User::create_bucket() to Bucket::create()"
    (first in v19.1.0), changed that return to `-ERR_BUCKET_EXISTS`.
  - The same code is on main (`rgw_sal_rados.cc:219-220`,
    `rgw_rest_s3.cc:2822-2826`, 7ed73efc1be, 2026-09-25).
- **Releases:** every Squid and Tentacle release; checked at v18.2.8, which
  answers 409, and at v19.2.6, v20.2.4 and main.
- **rgw-go:** phase 1 (unit M) answers 409 BucketAlreadyExists whenever
  another owner holds the name, whether it is found at the read or at the
  create. That is a difference from radosgw, which unit M records in
  `docs/exclusions.md`.
- **Upstream:** [#76398](https://tracker.ceph.com/issues/76398) (Fix Under
  Review), on CreateBucket's error codes for an existing bucket, and its open
  fix [ceph/ceph#68722](https://github.com/ceph/ceph/pull/68722) were both
  opened on 2026-05-01, before our sessions. Neither mentions the race, but
  #68722 covers it: `RadosBucket::create` returns `-EEXIST` for another
  owner, which CreateBucket answers with 409 BucketAlreadyExists, and its new
  handling of `-ERR_BUCKET_EXISTS` applies only to the same owner, whatever
  `rgw_bucket_eexist_override` says. Until it merges, radosgw answers 200 by
  default on v19.2.6, v20.2.4 and main. We file nothing, neither a duplicate
  issue nor a competing pull request. The return came with e2eb66a3617 in
  [ceph/ceph#50599](https://github.com/ceph/ceph/pull/50599).
  `rgw_bucket_eexist_override`
  ([#70369](https://tracker.ceph.com/issues/70369),
  [ceph/ceph#62186](https://github.com/ceph/ceph/pull/62186), in v21.0.0 and
  later, off by default) answers 409 to every `-ERR_BUCKET_EXISTS`, the race
  included.
- **Found:** phase 1 planning of unit M, 2026-09-29; derived from the source
  and verified 2026-09-30. The rgw-bug-reproduction session confirmed the
  mechanism at v19.2.6, v20.2.4 and main, and checked that #68722 changes
  the race's path; it was not reproduced on a running cluster.

## radosgw's admin API bypass-gc removal leaks a tail and runs unbounded

- **Kind:** defect, unfixed through main, with a fix in review.
- **Evidence:** at v19.2.6 `RGWOp_Bucket_Remove::execute` passes `bypass-gc`
  to `RGWBucketAdminOp::remove_bucket` without calling `set_max_aio`
  (`rgw_rest_bucket.cc:225-248`), so `max_aio` keeps its default of 0
  (`rgw_bucket.h:248`) and reaches `RadosBucket::remove_bypass_gc` unclamped
  (`rgw_bucket.cc:1303-1304`); the index checks, which read the same value,
  clamp it with `std::max(1, ...)` (`:594`, `:804`). The purge starts its
  budget at that 0 (`rgw_sal_rados.cc:505`) and tests `max_aio--` before each
  location of an object's manifest (`:542`).
  - On the first object whose manifest it walks, the test reads 0, so none of
    that object's tail stripes is released, yet its head and index entry are
    removed (`:565-566`; `delete_obj_aio`, `rgw_rados.cc:10687-10732`) and
    nothing is sent to GC. The stripes are left with neither an index entry
    nor a GC entry, where only an orphan scan finds them.
  - The budget then stays negative, so the drains at `:543-550` and `:573-580`
    never run: every later tail and head delete is issued without waiting
    until the final drain (`:585`), and nothing bounds how many are in flight.
  - `radosgw-admin bucket rm --bypass-gc` sets the budget from
    `--max-concurrent-ios`, 32 by default (`rgw_admin.cc:3519`, `:6619`), and
    is not affected unless given 0.
  - The same holds at v20.2.4 (`rgw_rest_bucket.cc:225-248`,
    `rgw_bucket.h:240`, `rgw_sal_rados.cc:526` and `:563-571`) and on main
    (`rgw_sal_rados.cc:553` and `:590-598`, 2026-09-29). On main, a9b9a52ee02
    (#80213, in no release yet) also forwards `bypass-gc` to the metadata
    master, whose admin op then purges that zone's copy the same way, so a
    removal started on a secondary zone meets the defect even from
    radosgw-admin.
- **Releases:** v19.2.3 through v19.2.6, and v20.1.0 and later, through
  v20.2.4 and main. The admin op gained `bypass-gc` with 9ae2d8c4e95
  (ceph/ceph#60227, first in v20.1.0) and its squid backport e2a2aba0883
  (ceph/ceph#62994, first in v19.2.3); before them it ignored the parameter
  (v19.2.2 `rgw_rest_bucket.cc:246`). The loop dates from b7a69fca248
  (v11.0.0); before the admin op reached it, only radosgw-admin did, with its
  non-zero budget.
- **rgw-go:** phase 1's admin API (unit N) honours `bypass-gc` with its own
  bounded deletion: it releases every object's tail stripes with at most
  `rgw_gc_max_concurrent_io` deletes in flight, counts a missing stripe as
  deleted and sends the tail deletes with `pool_full_try`, as the GC worker
  does (`rgw_gc.cc:371`, `:413-415`, `:677`). N's purge task records the
  difference in `docs/exclusions.md`.
- **Upstream:** we filed [#81304](https://tracker.ceph.com/issues/81304).
  The fix in review is
  [ceph/ceph#72304](https://github.com/ceph/ceph/pull/72304) (draft), which
  clamps the budget in `remove_bypass_gc` with `std::max(1, ...)`, as the
  index checks already do. Related but distinct:
  [#24789](https://tracker.ceph.com/issues/24789) and
  [#40587](https://tracker.ceph.com/issues/40587), ENOENT on an already
  missing stripe, and [#73348](https://tracker.ceph.com/issues/73348), copied
  objects. No earlier tracker issue or pull request reports it (full-text
  tracker and all-time pull-request search, 2026-09-29). The admin
  op reached the loop through
  [ceph/ceph#60227](https://github.com/ceph/ceph/pull/60227) and its squid
  backport [ceph/ceph#62994](https://github.com/ceph/ceph/pull/62994); the
  forwarding on main came with
  [ceph/ceph#71016](https://github.com/ceph/ceph/pull/71016), the fix for
  [#80213](https://tracker.ceph.com/issues/80213).
- **Found:** phase 1 planning of unit N, 2026-09-29; derived from the source.
  The rgw-bug-reproduction session reproduced it on running v19.2.6 and
  v20.2.4 clusters on 2026-10-01. It removed a bucket of five 16 MiB
  multipart objects through the admin API with `bypass-gc` and
  `purge-objects`. The bucket was gone, but four tail stripes of one object
  remained (two `__multipart_`, two `__shadow_`). The same removal through
  `radosgw-admin` left none.

## radosgw's bypass-gc bucket removal fails once a tail stripe is gone

- **Kind:** defect, a regression; unfixed through main.
- **Evidence:**
  - At v19.2.6 `remove_bypass_gc` releases each tail stripe with
    `cls_refcount_put` (`delete_tail_obj_aio`, `rgw_rados.cc:10659-10685`). A
    stripe that no longer exists fails the put with ENOENT, since
    `read_refcount` passes on every getxattr error but ENODATA
    (`cls_refcount.cc:26-34`, `:100-103`) and the OSD's getxattr answers
    ENOENT for a missing object (`BlueStore.cc:13247-13251`). `drain_aio`
    waits for every pending delete and returns the failure
    (`rgw_sal_rados.cc:98-112`), and the removal returns it at that drain
    (`:543-548`, `:573-578` or `:585-589`), before it removes the bucket. The
    GC worker counts the same ENOENT as done (`rgw_gc.cc:413-415`). The same
    at v20.2.4 (`rgw_rados.cc:11596-11622`, `rgw_sal_rados.cc:107-121`,
    `rgw_gc.cc:428-430`) and on main.
  - radosgw-admin drains after every 32 stripes by default, so the failure can
    stop it partway through an object, whose head and index entry stay while
    some of its stripes are gone; a retry walks that object again and can fail
    the same way, as #40587 describes for a rerun after an interrupted
    removal. radosgw-admin discards the removal's result
    (`rgw_admin.cc:8768-8779`) and exits 0 after logging the drain error.
  - Through the admin API, whose zero budget leaves only the final drain
    (previous entry), all the removal's deletes have been issued when the
    failure surfaces. The op answers 404 NoSuchBucket
    (`rgw_rest_bucket.cc:245-247`, `rgw_common.cc:98`) for a bucket that still
    exists, after removing every head whose manifest it walked, and a retry
    removes it: the purge skips a listed entry whose head is gone
    (`rgw_sal_rados.cc:522-525`), and `remove` skips a missing object
    (`:385-392`).
  - Stripes go missing when an earlier bypass-gc removal stopped partway, and,
    before 1fba459071d (#73348; v19.2.4, v20.2.1 and v21.0.0), when such a
    removal deleted stripes that a server-side copy in another bucket still
    shared. That commit replaced `cls_rgw_remove_obj`, which answers ENOENT
    for a missing object too (v19.2.2 `cls_rgw.cc:2432-2433`,
    `PrimaryLogPG.cc:8206-8207`).
  - It is a regression. The fix for #40587, bcdd7e63416 (ceph/ceph#28789),
    made the three drains ignore ENOENT on main on 2019-08-01
    (`rgw_bucket.cc:839`, `:868` and `:878` at a3039beaba8^1), but merging
    ceph/ceph#29118 on 2019-08-13 (a3039beaba8) took that branch's drains,
    which predate the fix (`rgw_bucket.cc:506`, `:535` and `:545`), so no
    release from v15.1.0 on has it. Its backports reached luminous in
    v12.2.13, mimic in v13.2.7 and nautilus in v14.2.5.
- **Releases:** every release from v15.1.0 on, checked at v15.1.0, v15.2.0,
  v15.2.17, v16.2.15, v17.2.9, v18.2.8, v19.2.6, v20.2.4 and main; before the
  fix, every release from v11.0.0 through luminous v12.2.12, mimic v13.2.6 and
  nautilus v14.2.4.
- **rgw-go:** phase 1's purge (unit N) counts a missing tail stripe as
  deleted, as the GC worker does, so a removal that meets one completes. N's
  purge task records the difference in `docs/exclusions.md`.
- **Upstream:** [#24789](https://tracker.ceph.com/issues/24789), open since
  2018 with no pull request. [#40587](https://tracker.ceph.com/issues/40587)
  reported it again in 2019 and is marked resolved by
  [ceph/ceph#28789](https://github.com/ceph/ceph/pull/28789), whose change the
  merge of [ceph/ceph#29118](https://github.com/ceph/ceph/pull/29118) lost;
  its backports, [ceph/ceph#30198](https://github.com/ceph/ceph/pull/30198)
  (luminous), [ceph/ceph#29984](https://github.com/ceph/ceph/pull/29984)
  (mimic) and [ceph/ceph#29956](https://github.com/ceph/ceph/pull/29956)
  (nautilus), kept it. No open pull request restores it (full-text tracker and
  all-time pull-request search, 2026-09-29).
  [#73348](https://tracker.ceph.com/issues/73348), fixed by
  [ceph/ceph#65772](https://github.com/ceph/ceph/pull/65772), changed how the
  loop releases stripes but not the drains. The loss is not reported: it has
  not been reproduced on a running cluster, and a report needs a live
  reproduction and a C++ reproducer.
- **Found:** phase 1 planning of unit N, 2026-09-29; derived from the source,
  not reproduced.

## The 2pc queue's reserved size drifts upward

- **Kind:** defect, fixed.
- **Evidence:**
  - `cls_2pc_queue` reserve adds size+10 per entry, but commit, abort and
    expire subtract only size, so `reserved_size` grows until reservations
    fail with ENOSPC (v19.2.3 `cls_2pc_queue.cc:137` against `:300`, `:401`
    and `:543`).
  - The fix is two commits on each branch: b97fe168f62 and 8f86e0926f4 on
    squid, first released in v19.2.4; their backports 98ed23288db and
    7a8f84046b8 on tentacle, first released in v20.2.3; and 00ad83d3ab2 and
    7f4eaee30cb on main, in v21.0.0 and later.
  - The fix's one-time recompute can be skipped; see the next entry.
- **Releases:** v19.2.3 and earlier, v20.2.2 and earlier. Drift accrued
  before an upgrade can outlive it.
- **rgw-go:** never assumes `reserved_size` is exact. `docs/exclusions.md`
  records the accounting per release.
- **Upstream:** fixed by the commits above; no tracker issue.
- **Found:** reported by rgw-rs; verified against the tags in the phase 0
  final review. The per-branch fix commits were reported by rgw-rs (rados-rs
  CEPH-BUG-001) and verified 2026-09-27.

## The 2pc queue's self-heal is skipped when another write comes first

- **Kind:** defect, unfixed through main, with a fix in review.
- **Evidence:** at v19.2.6 reserve recomputes `reserved_size` from the
  outstanding reservations only when the queue head's urgent data decoded
  below struct version 3 (`cls_2pc_queue.cc:135`), and every write re-encodes
  it at version 3 (`cls_2pc_queue_types.h:72`). Commit
  (`cls_2pc_queue.cc:371`), abort (`:458`), expiry (`:602`) and entry removal
  (`:698`) re-encode it without recomputing. If one of them is the first
  write to a drifted queue after the upgrade, the drift stays for good, and
  with it the spurious ENOSPC. The same holds at v20.2.4 (`:137`) and on
  main.
- **Releases:** v19.2.4 and later, v20.2.3 and later, and v21.0.0 and later,
  on a queue that drifted under an earlier release.
- **rgw-go:** has no 2pc queue client yet. Phase 3 runs the stale-reservation
  expiry on every persistent queue, which is one of the writes that keeps a
  drift, and must not assume `reserved_size` is exact (`docs/exclusions.md`).
- **Upstream:** [#80994](https://tracker.ceph.com/issues/80994). A
  maintainer's comment there calls it a duplicate of the drift fix for
  [#74713](https://tracker.ceph.com/issues/74713), but that fix is what added
  the reserve-only recompute, and every branch still recomputes only in
  reserve. The fix in review is
  [ceph/ceph#72212](https://github.com/ceph/ceph/pull/72212) (open), opened
  on 2026-09-29, after #80994, by the issue's assignee. It recomputes the
  reserved size on every write path, not only in reserve. We open no
  competing pull request.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-002); verified 2026-09-27.

## The 2pc queue hands out reservation id 0, which radosgw treats as none

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 a reservation id is a u32 whose value 0 is `NO_ID`
  (`cls_2pc_queue_types.h:9-10`), and reserve pre-increments `last_id`
  without skipping 0 (`cls_2pc_queue.cc:186`), so the 2^32nd reservation on
  one queue, the one after id 2^32-1, gets id 0. radosgw's publisher skips
  both the commit and the abort of a reservation whose id is `NO_ID`
  (`rgw_notify.cc:1173-1174` and `:1283-1284`): the event is lost, and its
  space stays reserved until radosgw's stale-reservation cleanup, which every
  30 seconds frees reservations older than 120 seconds (`:814-815`). A
  wrapped id that collides with a live reservation answers -EAGAIN
  (`cls_2pc_queue.cc:192-196`), and nothing in radosgw retries it. The same
  holds at v20.2.4 (`cls_2pc_queue_types.h:11-12`, `cls_2pc_queue.cc:188`,
  `rgw_notify.cc:1187-1188` and `:1298-1299`) and on main
  (`cls_2pc_queue_types.h:17-18`, `cls_2pc_queue.cc:188`,
  `rgw_notify.cc:1221-1222` and `:1334-1335`).
  - Reproduced on v19.2.6 and v20.2.4 with the queue head's `last_id` written
    directly to 0xFFFFFFFF, since 2^32 real reservations are impractical: the
    next notification PUT answers 200 but its event is never queued (topic
    stats show one reservation and no new entry), the head holds reservation
    id 0 for its 4 KiB, the PUT after it gets id 1 and is queued, and the
    cleanup frees the space two to two and a half minutes later. A direct
    `cls_2pc_queue_reserve` call returns 0 with id 0.
- **Releases:** every release checked, v19.2.6 through main.
- **rgw-go:** has no notification publisher yet. Phase 3's must not use id 0
  as "no reservation"; the class commits id 0 like any other.
- **Upstream:** [#80996](https://tracker.ceph.com/issues/80996), whose
  confirmation note carries the live runs and a C++ reproducer, `repro.cc`;
  its fix, [ceph/ceph#72163](https://github.com/ceph/ceph/pull/72163), skips
  NO_ID when the id wraps, adds no test, and is in review.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-003); verified 2026-09-27;
  reproduced 2026-09-29 on disposable Squid and Tentacle clusters.

## radosgw's notification queue listing never pages past 1024 queues

- **Kind:** defect, fixed in v20.2.3 and v21.0.0, not on squid.
- **Evidence:** at v19.2.6 `read_queue_list` reads the queue registry's omap
  keys 1024 at a time but never advances `start_after` (`rgw_notify.cc:108`,
  used at `:114`), so with more than 1024 queues registered it re-reads the
  first page for ever. Ceph commit b984980897d, "rgw/notify: fix reading the
  entries in a loop" (ceph/ceph#66246), advances it and cites tracker #73812;
  it is in v21.0.0 and later, and its backport 15de1799510 (ceph/ceph#66491)
  is in v20.2.3 and later (v20.2.4 `rgw_notify.cc:130`). It is not on the
  squid branch as of a742f50 (2026-09-03).
- **Releases:** every Squid release through v19.2.6; v20.2.0 through v20.2.2.
- **rgw-go:** cannot fix a coexisting radosgw; `docs/exclusions.md` records
  the hazard. Phase 3's own reading of the registry must advance its marker.
- **Upstream:** [#73812](https://tracker.ceph.com/issues/73812), fixed by
  [ceph/ceph#66246](https://github.com/ceph/ceph/pull/66246). Its squid
  backport, [#73893](https://tracker.ceph.com/issues/73893), is under review
  as [ceph/ceph#66345](https://github.com/ceph/ceph/pull/66345); the tentacle
  backport, [#73894](https://tracker.ceph.com/issues/73894), is resolved by
  [ceph/ceph#66491](https://github.com/ceph/ceph/pull/66491).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-004); verified 2026-09-27.
  `docs/exclusions.md` already recorded it.

## Squid's realm reload hangs when a pubsub HTTP push has lost its wakeup

- **Kind:** defect, fixed in Tentacle, not on squid. Unreproduced: the hang
  was seen once and its cause is inferred from the code.
- **Evidence:**
  - At v19.2.6 `rgw_http_req_data::wait` checks `done` without the lock
    (`rgw_http_client.cc:74-76`). For a yield context it then stores the
    completion under the lock without checking again (`:64-71`, `:77-82`).
    `finish()` posts a stored completion and otherwise signals a condition
    variable that no coroutine waits on (`:110-116`). If `finish()` runs
    between the check and the store, the coroutine is never resumed.
  - A persistent topic's HTTP push holds a shared lock on
    `s_http_manager_mutex` across that wait (`rgw_pubsub_push.cc:94`,
    `:117-119`). A realm reload's teardown calls `rgw::notify::shutdown` near
    its end, followed only by `v1_topic_migration.stop()`
    (`rgw_rados.cc:1102-1105`). `rgw::notify::shutdown` takes the lock
    exclusively in `shutdown_http_manager` (`rgw_pubsub_push.cc:423-429`,
    called from `rgw_notify.cc:839`) before it stops the notification manager
    (`:840`). A stranded push therefore blocks the reload for ever after
    "Frontends paused" and before "driver closed" (`rgw_realm_reloader.cc:95`,
    `:101`, `:104`).
  - A failing persistent push is retried with no pause by default: the three
    `rgw_topic_persistency_*` options default to 0 (`rgw.yaml.in:3971-4003`),
    and a queue that holds entries is read again without the idle sleep
    (`rgw_notify.cc:376-383`, `:458`). An endpoint that refuses connections
    therefore drives the racy wait in a tight loop.
  - Seen once, on 2026-09-29, on a disposable Squid cluster. For about five
    minutes a radosgw ran the reproduction of
    [#80996](https://tracker.ceph.com/issues/80996), a persistent topic
    pushing to `http://127.0.0.1:1`. Two hours later it received a period
    commit, logged "Frontends paused" and never logged "driver closed"; it
    was still paused when its pod was deleted about eight minutes later.
    Every thread that `RGWRados::finalize` stops before
    `rgw::notify::shutdown` had exited, as had the AMQP and Kafka managers.
    The pubsub http_manager and notif-worker-0 threads were still alive, and
    the notification manager was still cycling. No stacks were captured.
  - At v20.2.4 `wait()` checks `done` under the lock, and `async_wait` stores
    the completion before releasing it (`rgw_http_client.cc:65-79`). This is
    Ceph commit 92dd2b9c380, "rgw/http: check 'done' under mutex", in v20.0.0
    and later. The lock path is otherwise the same (`rgw_pubsub_push.cc:97`,
    `:124`, `:432-438`).
- **Releases:** v19.1.1 (a release candidate) and v19.2.0 through v19.2.6,
  and the squid branch as of a742f50616e (2026-09-03). The unlocked check
  dates from 57887f6a364, "rgw: http client drops mutex before suspending
  coroutine", first in v15.1.0. The lock that turns it into a reload hang
  arrived with 220bd93999b, the squid fix for #65337, first in v19.1.1. Reef
  has the race but no pubsub lock (v18.2.7 `rgw_pubsub_push.cc:85-108`).
  Tentacle is not affected.
- **rgw-go:** cannot fix a coexisting radosgw. `hack/rooket/populate.sh`
  waits for the reload its period commit starts and, when it does not
  finish, stops with the command that restarts the radosgw. rgw-go has no
  notification publisher before phase 3. Phase 3's must bound every push
  with a deadline, must not hold a lock across a push that its shutdown takes
  exclusively, and must cancel outstanding pushes on shutdown.
- **Upstream:** no issue reports this stall. Not filed: filing waits on a
  reproduction on a running system and a C++ reproducer. The fix,
  [ceph/ceph#57632](https://github.com/ceph/ceph/pull/57632) (merged
  2024-06-27), cites no tracker issue and has no squid backport, open or
  merged. [#66100](https://tracker.ceph.com/issues/66100) has the same
  symptom but stalls earlier, joining the data-sync thread. Its squid
  backport, [#74738](https://tracker.ceph.com/issues/74738)
  ([ceph/ceph#67439](https://github.com/ceph/ceph/pull/67439)), is not in
  v19.2.6. The fix for [#65337](https://tracker.ceph.com/issues/65337)
  created the lock path. Searched 2026-09-29: the tracker's full text for
  `shutdown_http_manager`, `RGWHTTPManager::stop`, `notify::shutdown`,
  `s_http_manager_mutex`, `rgw_http_req_data`, "Frontends paused" and
  realm-reload hang, deadlock and stuck; and ceph/ceph pull requests, by
  keyword and, for every open `rgw` pull request and every one closed since
  2025-10-01, by the files they change.
- **Found:** a period commit during phase 1 cluster work, 2026-09-29;
  analysed 2026-09-29; not reproduced.

## radosgw's realm reload waits out the notification manager's timers

- **Kind:** defect, fixed in v21.0.0, not on squid or tentacle.
  Unreproduced: derived from the source; the reload times measured fit it.
- **Evidence:**
  - At v19.2.6 `Manager::stop()` sets `shutdown`, releases the work guard and
    joins the worker (`rgw_notify.cc:755-759`), but cancels no timer.
    `process_queues` waits between queue-list reads on a timer of 30 s plus
    100-500 ms of jitter (`:645-661`, `Q_LIST_UPDATE_MSEC` at `:809`), and
    each owned queue's `cleanup_queue` on one of 30 s (`:296-299`, `:815`).
    The worker's `io_context.run()` (`:775`) returns only after they fire, so
    `rgw::notify::shutdown`, which a realm reload's teardown calls near its end
    (see the previous entry), waits up to about 30.5 s with the frontends
    paused.
  - The same at v20.2.4 (`rgw_notify.cc:767-771`, `:657-673`, `:821`,
    `:305-308`, `:827`, `:787`).
  - Ceph commit 433717a2480, "rgw/notifications: allow for graceful shutdown
    of notification manager", makes `stop()` wait 2 s for the worker and then
    stop its `io_context` (main `rgw_notify.cc:793-810`, 2026-09-29). It is
    in v21.0.0 and later.
  - Reloads measured on disposable clusters on 2026-09-29, each from the
    radosgw's "Frontends paused" log line to its "driver closed" line, took
    10.0, 12.9 and 28.1 s on v19.2.6 and 2.7, 3.7 and 26.5 s on v20.2.4, all
    within that bound.
- **Releases:** Squid and Tentacle, checked at v19.2.6 and v20.2.4 and at the
  squid and tentacle branch heads (a742f50616e, 2026-09-03; 9208ed9a291,
  2026-09-23). v21.0.0 and later are not affected.
- **rgw-go:** cannot shorten a coexisting radosgw's reload.
  `hack/rooket/populate.sh` allows 180 s for it. Phase 3's notification
  manager must cancel its timers when it stops.
- **Upstream:** [#71963](https://tracker.ceph.com/issues/71963), fixed on main
  by [ceph/ceph#63986](https://github.com/ceph/ceph/pull/63986); the issue's
  pull-request field names
  [ceph/ceph#63909](https://github.com/ceph/ceph/pull/63909), an unrelated
  bucket-logging change. The squid backport,
  [#72004](https://tracker.ceph.com/issues/72004), is New. The tentacle
  backport, [#72003](https://tracker.ceph.com/issues/72003), is marked
  Resolved, but the pull request it names,
  [ceph/ceph#66769](https://github.com/ceph/ceph/pull/66769), carries only
  bucket-logging changes, and the tentacle branch head still has the old
  `stop()`.
- **Found:** analysis of the previous entry's hang, 2026-09-29; derived from
  the source, not reproduced.

## radosgw caches any control-pool UPDATE_OBJ notify payload unchecked

- **Kind:** quirk. The cache-coherence protocol trusts intra-cluster notifies
  by design; the surprise is that read access to the control pool is enough to
  plant a cache entry.
- **Evidence:** at v19.2.6 `RGWSI_SysObj_Cache::watch_cb`
  (`svc_sys_obj_cache.cc:465`) decodes the notify's `RGWCacheNotifyInfo` and,
  on `UPDATE_OBJ`, stores its `obj_info` straight into the cache (`:490-491`),
  checking neither a signature nor the `notifier_id` it is passed (`:468`).
  `RGWWatcher::handle_notify` runs the callback and acks unconditionally
  (`svc_notify.cc:77` and `:80`). A RADOS notify is a read op (`rados.h:258`,
  `NOTIFY = __CEPH_OSD_OP(RD, DATA, 6)`), so read caps on the zone's control
  pool are enough to send one; a planted entry lives
  `rgw_cache_expiry_interval` (default 900 s, `rgw.yaml.in:3324`). The same at
  v20.2.4 (`svc_sys_obj_cache.cc:490-491`).
- **Releases:** every release checked, v19.2.2 through main.
- **rgw-go:** phase 1's cache-notify handling (unit M) must treat a control
  notify as an invalidate-and-reread and never apply the notify payload as
  truth, and must keep its cephx caps on the control pool narrow.
  `docs/exclusions.md` records cache invalidation as a coexistence obligation.
- **Upstream:** not filed; the behaviour is radosgw's design.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-017); verified 2026-09-27.

## radosgw aborts the process after 100 failed control-watch re-registrations

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 `RGWWatcher::reinit` calls `abort()` once
  `retries > 100` (`svc_notify.cc:89-93`). `retries` is a per-watcher counter
  initialised to 0 (`:36`) and incremented on every failed register or
  non-ENOENT unregister (`:103`, `:111`); nothing resets it, so it counts
  failures over the whole process lifetime, and each failure reschedules
  `reinit` at once with no backoff (`handle_error` → `C_ReinitWatch`,
  `:82-86`; `:104`, `:112`). An unwatch that answers ENOENT is the
  exception: `reinit` then returns and reschedules nothing (`:99-101`),
  which abandons the watch (see "radosgw abandons a control watch whose
  control object is deleted"). The counter and abort arrived with
  ff248d7ed94, "rgw: Try to handle unwatch errors sensibly", first released
  in v19.2.3, and its main-line twin 34366f0f0d8, first released in
  v20.1.0; v19.2.2's `reinit` has no counter and reschedules for ever
  (`:88-101`). The same at v20.2.4 (`svc_notify.cc:87-91`). The default
  `rados_osd_op_timeout` is 0 (`global.yaml.in:6379`), so a watch op blocks
  rather than fails during a transient OSD outage and does not by itself reach
  the counter.
- **Releases:** v19.2.3 and later, v20.1.0 and later, through v19.2.6, v20.2.4
  and main; Squid before v19.2.3 does not abort. Reef got the same change in
  v18.2.8 (c95ea88269d), below rgw-go's floor.
- **rgw-go:** phase 1's driver control-watch re-registration (unit M) must back
  off between attempts and retry without ever aborting the process, and must
  not count re-registration failures unboundedly over the process lifetime.
- **Upstream:** [#80992](https://tracker.ceph.com/issues/80992).
  [ceph/ceph#68207](https://github.com/ceph/ceph/pull/68207) (2026-04) reset
  the counter on success, among other reinit fixes, but the stale bot closed
  it unreviewed; it was filed for
  [#73564](https://tracker.ceph.com/issues/73564), a different symptom (a
  watch2 segfault). The draft
  [ceph/ceph#72251](https://github.com/ceph/ceph/pull/72251) revives it with
  abhishek593's commit unchanged and fixes both #73564 and #80992: it resets
  `retries` on a successful re-registration, returns after a failed
  `unregister_watch()`, and schedules one `C_ReinitWatch` at a time through
  an atomic `reinit_pending` flag, with a move constructor added because
  `RGWSI_Notify` holds a `std::vector<RGWWatcher>`.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-018); verified 2026-09-27;
  derived from the source, not reproduced on a running cluster, which would
  take over 100 failures in one radosgw's lifetime.

## radosgw abandons a control watch whose control object is deleted

- **Kind:** defect, a regression: the abandonment #59217 reported, which
  ceph/ceph#50707 fixed in 2023, reintroduced for ENOENT by
  ceph/ceph#62253; unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - Deleting a control object disconnects its watchers (`_delete_oid`,
    `osd/PrimaryLogPG.cc:8255-8264`, `:8332-8341`), and the client hands
    each watch ENOTCONN (`osdc/Objecter.cc:924-928`, `:961-966`).
  - `RGWWatcher::handle_error` removes the watcher and schedules `reinit`
    (`rgw/services/svc_notify.cc:82-87`, `:80-85`). Removing it disables the
    metadata cache: `remove_watcher` calls `_set_enabled(false)`
    (`:354-365`, `:341-352`), which reaches `ObjectCache::set_enabled`
    through the cache's callback (`:386-392`, `:373-379`;
    `rgw/services/svc_sys_obj_cache.cc:30-32` and `:504-507` at both), and
    that drops every entry and makes every lookup miss
    (`rgw/rgw_cache.cc:303-311` and `:18` at both).
  - The watch had registered, so `unregister_done` is false (`:34`, `:156`,
    `:166`; `:131`) and `reinit` first unregisters it (`:95-96`, `:93-94`).
    The unwatch is a WATCH op, and the OSD answers ENOENT to any WATCH op on
    a missing object (`osd/PrimaryLogPG.cc:6967-6969`, `:7036-7038`).
  - `reinit` takes ENOENT from the unwatch for a shutdown, "Going down there
    is no such watch", and returns without registering, rescheduling or
    counting a retry (`:99-101`, `:97-99`). Only `handle_error` and `reinit`
    itself schedule `C_ReinitWatch` (`:86`, `:104`, `:112`; `:84`, `:102`,
    `:110`), and a watch that is not registered gets no further error, so
    nothing tries again.
  - radosgw creates control objects only at startup (`init_watch`'s
    `create(false)`, `:231-238`, `:198-205`). The watch stays down, and the
    cache off, until radosgw restarts, even after another gateway's startup
    or rgw-go creates the object again. Every metadata read then goes to
    RADOS; nothing is served stale.
  - Before 2023 `reinit` returned on any failed unwatch, without
    re-registering or rescheduling (`svc_notify.cc:87-98` at f9aae71af3a~1),
    which #59217 reported. f9aae71af3a (ceph/ceph#50707, "Fixes:
    https://tracker.ceph.com/issues/59217") removed that return and made a
    failed register reschedule. "rgw: Try to handle unwatch errors sensibly"
    put the return back for ENOENT alone, its message reading "IF we get
    `-ENOENT` from unwatch just stop trying to renew": 34366f0f0d8 on main,
    first released in v20.1.0, ff248d7ed94 on squid, first released in
    v19.2.3, and c95ea88269d on reef, first released in v18.2.8. v19.2.2's
    `reinit`, between the two, logs the failed unwatch, re-registers, and
    reschedules while the object is missing (`:88-101`), so it recovers once
    the object exists again.
- **Releases:** v18.2.8, v19.2.3 and later, and v20.1.0 and later, through
  v19.2.6, v20.2.4 and main (checked 2026-10-01); v19.2.2 and v18.2.7
  recover.
- **rgw-go:** when a registration fails with ENOENT, the driver creates that
  control object again with startup's `create(false)` and keeps
  re-registering with its backoff (`internal/driver/notify.go`'s
  `watchLoop`), so its watch comes back. `docs/exclusions.md` records the
  difference.
- **Upstream:** pending: sent to rgw-bug-reproduction, which decides
  whether to reopen #59217 or file the regression new.
  [#59217](https://tracker.ceph.com/issues/59217), "metadata cache: if a
  watcher is disconnected and reinit() fails, it won't be retried again",
  reported the abandonment, and
  [ceph/ceph#50707](https://github.com/ceph/ceph/pull/50707) fixed it
  (merged 2023-04-06), backported by
  [ceph/ceph#51017](https://github.com/ceph/ceph/pull/51017) (reef),
  [ceph/ceph#54014](https://github.com/ceph/ceph/pull/54014) (pacific) and
  [ceph/ceph#54015](https://github.com/ceph/ceph/pull/54015) (quincy).
  [ceph/ceph#62253](https://github.com/ceph/ceph/pull/62253) (merged
  2025-03-18; squid
  [ceph/ceph#62402](https://github.com/ceph/ceph/pull/62402), reef
  [ceph/ceph#62403](https://github.com/ceph/ceph/pull/62403)), made for
  [#70422](https://tracker.ceph.com/issues/70422), "radosgw crashes trying
  to renew watch on an object that does not exist", reintroduced it for
  ENOENT, which it treats as a shutdown. No issue or pull request reports
  the regression or changes the early return; the draft
  [ceph/ceph#72251](https://github.com/ceph/ceph/pull/72251), the fix for
  #80992, and the closed
  [ceph/ceph#68207](https://github.com/ceph/ceph/pull/68207) it revives
  leave the `-2` return untouched (searched 2026-10-01). Each search method
  was first run on a known match.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds #80992 by
    `reinit` and #70422 by the quoted phrase `"unwatch2() returned"`, which
    appears only in its description. Terms: `reinit` (124 issues, 18 in
    rgw), `unregister_watch` (26, 3 in rgw), `C_ReinitWatch`, `RGWWatcher`,
    and the quoted phrases `"no such watch"`,
    `"Going down there is no such watch"`, `"unregister_watch() returned"`
    and `"register_watch() returned"`. Besides #59217 and #70422, the
    nearest are #80992, and #73361 with its backports, a crash when
    `C_ReinitWatch` races `finalize_watch`.
  - ceph/ceph pull requests through `gh search prs`, which finds
    ceph/ceph#72251 and ceph/ceph#68207 by `reinit`. Terms: `reinit`
    (finding ceph/ceph#50707), `unregister_watch` (finding it and its three
    backports), `C_ReinitWatch`, `RGWWatcher`, `RGWSI_Notify`, `svc_notify`,
    `"unwatch errors"` (ceph/ceph#62253 and its backports) and
    `"no such watch"` (none, the phrase being only in the code).
- **Found:** phase 1 unit M, Task 2's re-review, 2026-10-01, as a regression
  of an upstream report and fix; derived from the source, not reproduced.

## radosgw keeps only the low 32 bits of rgw_num_control_oids

- **Kind:** quirk. Unreproduced: derived from the source.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `rgw_num_control_oids` is a `type: int` option with no `min` or `max`
    and `with_legacy: true` (`common/options/rgw.yaml.in:1115-1129`,
    `:1174-1191`), so its legacy field is an `int64_t` (`OPTION_OPT_INT`,
    `common/config_values.h:41` at both).
  - `init_watch` assigns it to `int num_watchers`
    (`rgw/services/svc_notify.h:37`, `:33`; `rgw/services/svc_notify.cc:201`,
    `:165`). Ceph builds as C++20 (`src/CMakeLists.txt:216`, `:243`), which
    defines the narrowing as modular, so only the low 32 bits count, read
    as a signed int.
  - The narrowed count then names the objects as any count does
    (`svc_notify.cc:203-221`, `:167-183`): 2^32 becomes 0, the single
    legacy object `notify`; 2^31 becomes -2^31, one object, `notify.0`;
    2^32+8 becomes 8, `notify.0` to `notify.7`. A value just below 2^31
    makes radosgw allocate and create that many control objects at startup.
- **Releases:** v19.2.6 and v20.2.4, the tags checked.
- **rgw-go:** matches it: `controlOIDs` keeps the low 32 bits the same way
  (`internal/driver/notify.go`), so both gateways name the same control
  objects for any setting. It is reachable only by misconfiguration.
- **Upstream:** not filed; a quirk.
- **Found:** phase 1 unit M, Task 2, 2026-10-01; derived from the source, not
  reproduced.

## radosgw adds STANDARD to an empty placement target on decode

- **Kind:** quirk.
- **Evidence:** at v19.2.6 the binary decoder of a zonegroup placement target
  inserts `STANDARD` when `storage_classes` is empty (`rgw_zone_types.h:624`),
  and the JSON decoder does the same (`rgw_zone.cc:757`). So a zonegroup that
  radosgw decodes and re-encodes is longer than the stored bytes: 448 bytes
  against 436 on the phase 0 cluster. ceph-dencoder round-trips the stored
  bytes unchanged.
- **Releases:** Squid and Tentacle.
- **rgw-go:** encodes what is stored, matching ceph-dencoder. The phase 0
  gate keeps a ceph-dencoder oracle for a zonegroup that hits this case; since
  populate added a cloud-tier storage class, the phase 0 clusters no longer
  do.
- **Upstream:** not filed; a quirk.
- **Found:** phase 0 gate, Task 15.

## radosgw lets a matching referer grant replace the requester's own ACL grants

- **Kind:** quirk, not security-relevant: the replacement is intended, the
  design by which a negative referer grant revokes access (last
  paragraph of Evidence), so it was not found by us. Unreproduced: derived
  from the source.
- **Evidence:** `rgw_acl.cc`, `rgw_acl.h` and `rgw_acl_swift.cc` are the same
  blobs at v19.2.6 and v20.2.4, so each of their lines below holds at both.
  - `RGWAccessControlPolicy::verify_permission` asks `get_perm` for
    `perm | RGW_PERM_READ_OBJS | RGW_PERM_WRITE_OBJS` (`rgw_acl.cc:208`). An
    S3 grant holds at most FULL_CONTROL, so unless Swift's container flags
    are granted too, the flags `get_perm` gathers from the user map, the
    owner and the groups fall short of that mask, and it calls
    `get_referer_perm` whenever the request has a Referer (`:190-192`).
  - `get_referer_perm` starts from those flags and assigns each matching
    referer grant's flags over them (`:137-158`). A match therefore discards
    the requester's own grant, the owner's implicit READ_ACP and WRITE_ACP,
    and the group grants alike.
  - A Swift container ACL is built on `create_default` for the user who
    sets it (`rgw_rest_swift.cc:706-717`; v20.2.4 `:704-715`), normally the
    owner, who so keeps FULL_CONTROL, and each `.r:host` entry of its read
    list becomes a referer grant holding `RGW_PERM_READ_OBJS`
    (`rgw_acl_swift.cc:21`, `:57-96`, `:146-149`, `:180-191`).
  - The bucket ACL check passes the request's `HTTP_REFERER`, and `perm` as
    both the mask and the permission (`rgw_common.cc:1128-1130`,
    `:1420-1423`; v20.2.4 `:1141-1143`, `:1451-1454`). No check before it
    spares the owner (`:1342-1372`, `:1405-1409`; v20.2.4 `:1366-1393`,
    `:1435-1439`). An account user's requests to its own account's buckets
    never consult ACLs (`:1397-1403`; v20.2.4 `:1427-1433`), so an owner is
    affected only as a user outside any account.
  - So on a bucket whose Swift read ACL is `.r:example.com` and which has no
    bucket policy, the owner's S3 PutObject (`rgw_op.cc:3982-3985`; v20.2.4
    `:4191-4194`) with a `Referer: http://example.com/page` header is refused
    with 403: its WRITE, from FULL_CONTROL, is replaced by READ_OBJS, which
    counts only as READ and READ_ACP (`rgw_acl.cc:216-221`). Without the header
    the same request succeeds. Over Swift the owner's PUT still succeeds: the
    check falls back to the user ACL (`rgw_common.cc:1427-1430`; v20.2.4
    `:1461-1466`), which radosgw loads only for Swift, as the bucket owner's
    account ACL, by default FULL_CONTROL for the owner, and checks with no
    Referer (`rgw_op.cc:588-590`, `:453-482`; v20.2.4 `:618-620`, `:483-512`).
  - The replacement came with 11d4370eaf7, "rgw: partially respect Swift's
    negative, HTTP referer-based ACLs", so that a negative `.r:-host` grant,
    stored with no flags, could revoke what `.r:*` gives through the
    AllUsers group.
- **Releases:** every release since v12.1.0; checked at v19.2.6 and v20.2.4,
  at the squid and tentacle branch heads (a742f50616e, 2026-09-03;
  c9579b573bc, 2026-09-29), which carry the same `rgw_acl.cc` and
  `rgw_acl.h`, and at main (fb19b5bced2, 2026-09-30), whose two files differ
  only in their modelines, one include and `generate_test_instances`.
- **rgw-go:** mirrors it, so that either gateway answers a request on a
  shared zone alike: `acl.List.RefererPerm` replaces the flags as
  `get_referer_perm` does, and the spec "lets a matching referer grant
  replace even the owner's own grant, as radosgw does"
  (`internal/acl/eval_test.go`) pins it.
- **Upstream:** intended behaviour, so there is nothing to file.
  [#18841](https://tracker.ceph.com/issues/18841) (Resolved) is the
  negative-referer report the replacement answered, through
  [ceph/ceph#14344](https://github.com/ceph/ceph/pull/14344), after a first
  attempt, [ceph/ceph#13294](https://github.com/ceph/ceph/pull/13294), was
  closed unmerged. No issue or pull request reports the owner's lost grant
  as a defect; searched 2026-09-29 and 2026-09-30, each method checked
  first on a known match:
  - the tracker's issue filter on any searchable field, every project and
    status, checked on `cls_otp_divzero_repro`, which only a comment on
    [#80948](https://tracker.ceph.com/issues/80948) holds: `get_referer_perm`
    and `referer_list` find nothing, `ACLReferer` only
    [#18685](https://tracker.ceph.com/issues/18685), its backport
    [#18895](https://tracker.ceph.com/issues/18895) and a cleanup
    ([#39619](https://tracker.ceph.com/issues/39619)), and `RGW_PERM_READ_OBJS`
    only [#18517](https://tracker.ceph.com/issues/18517), about Swift's default
    container ACLs. The tracker's full-text search for referer with acl, owner,
    denied, 403 and AccessDenied finds no report of it either; its closest hits
    are #18841 and #18685.
  - `gh search prs --repo ceph/ceph`, checked on a phrase that only
    #14344's body holds: `get_referer_perm` and `ACLReferer` find nothing,
    `referer_list` only #14344.
  - ceph/ceph pull requests by the files they change, for `src/rgw/rgw_acl.cc`
    and `src/rgw/rgw_acl.h`, checked on the merged #14344,
    [ceph/ceph#13005](https://github.com/ceph/ceph/pull/13005),
    [ceph/ceph#65374](https://github.com/ceph/ceph/pull/65374) and
    [ceph/ceph#65742](https://github.com/ceph/ceph/pull/65742): none of the 1561
    open ones touches either. Of the 3462 closed unmerged since 2024-01-01, only
    [ceph/ceph#43978](https://github.com/ceph/ceph/pull/43978) (the owner's
    `get_perm` flags widened to FULL_CONTROL, the referer step left as is) and
    [ceph/ceph#57539](https://github.com/ceph/ceph/pull/57539) (subuser
    permission masks) are ACL changes; the rest carry the two files among
    hundreds or thousands of others.
- **Found:** phase 1 unit Z, Task 6's transcription of `get_perm`,
  2026-09-29; derived from the source, not reproduced.

## radosgw ends a Referer's userinfo at an @ in its path

- **Kind:** defect, not security-relevant, unfixed through main.
  Unreproduced: derived from the source.
- **Evidence:** `ACLReferer::get_http_host` (`rgw_acl.h:253-270`, the same
  blob at v19.2.6 and v20.2.4) takes what follows the first `://`, drops
  everything through the first `@` after it, wherever that `@` falls, and
  ends the host at the first `/` or `:`. An `@` in the path or query thus
  ends the userinfo: `http://evil.example/x@good.example/` has the host
  `good.example`. A query is not cut either: `http://example.com?x` has the
  host `example.com?x`. `is_match` compares that host with a referer
  grant's spec (`:212-233`), so a Swift referer ACL (`.r:host` or
  `.r:-host`) is decided by what follows the `@`, not by the page's host:
  with `.r:good.example` on a container, a request whose Referer is the URL
  above matches the grant. The parse came with 941dfad6717, "rgw: swift: The
  http referer should be parsed to compare in swift API".
- **Releases:** every release since v12.0.0; checked at v19.2.6, v20.2.4, the
  squid and tentacle branch heads and main, as for the previous entry.
- **rgw-go:** mirrors it: `acl.Referer.IsMatch` parses the host as
  `get_http_host` does, and the spec "the first @ after the scheme ends the
  userinfo, even in the path" (`internal/acl/eval_test.go`) pins it.
- **Upstream:** no issue or pull request reports it. Not filed: filing
  waits on a reproduction on a running system and a C++ reproducer.
  [#18685](https://tracker.ceph.com/issues/18685)
  (Resolved) asked for the Referer's host to be compared rather than the whole
  value; its fix, [ceph/ceph#13005](https://github.com/ceph/ceph/pull/13005),
  wrote this parse. Searched with the previous entry's methods: the tracker
  filter finds `get_http_host` only in
  [#39619](https://tracker.ceph.com/issues/39619), a string_view cleanup, and
  the full-text search for referer with userinfo, host parse and hostname finds
  only #18685, its backports and unrelated reports; `gh search prs` finds
  `get_http_host` only in #13005 and in
  [ceph/ceph#50330](https://github.com/ceph/ceph/pull/50330), an unrelated
  `RGWEnv::get` fix; the file sweep is the previous entry's.
- **Found:** phase 1 unit Z, Task 6's transcription of `is_match`,
  2026-09-29; derived from the source, not reproduced.

## cls_version's header documents EAGAIN, but the class returns ECANCELED

- **Kind:** quirk. ECANCELED is the code radosgw relies on; the header
  comment is wrong.
- **Evidence:** at v19.2.6 `cls_version_client.h:19` says a conditional
  `inc` returns EAGAIN when its condition fails, but `inc` and `check` both
  return ECANCELED (`cls_version.cc:166` and `:196`). The same at v20.2.4,
  v21.1.0 and main.
- **Releases:** every release checked, v19.2.6 through main.
- **rgw-go:** follows the class. `internal/cls/version` documents
  `ErrCanceled` for `IncConds` and `Check`, and its integration spec asserts
  it.
- **Upstream:** not filed; the header comment is wrong, not the class.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-014); verified 2026-09-27.

## cls_lock get_info and assert_locked fail with EIO on an expired ephemeral lock

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 `read_lock` removes the object when an ephemeral
  lock has no unexpired holder left (`cls_lock.cc:93-98`). `get_info` and
  `assert_locked` are registered without the write flag (`:634-642`), and the
  OSD fails a class call that writes without it with EIO
  (`PrimaryLogPG.cc:6181-6184`). So both answer EIO rather than the lock's
  state, and the removal is dropped with them. Ephemeral locks date from
  a289f2d8654 (v14.1.0); both methods are still read-only on main
  (`cls_lock_ops.h:255` and `:257`).
- **Releases:** every release since v14.1.0; checked at v19.2.6, v20.2.4 and
  main.
- **rgw-go:** unaffected. `internal/cls/lock` binds both methods, as
  `GetInfo` and `AssertLocked`, but rgw-go takes no ephemeral lock and sends
  neither for one; the package documents the restriction on both and on
  `TypeExclusiveEphemeral`. The fakerados lock emulator removes an expired
  ephemeral lock's object as the class does, so a spec that reads one gets
  EIO as from the OSD. radosgw's one ephemeral lock, the bucket reshard lock
  (`rgw_reshard.cc:734`), is never read with either method; rgw-go must do
  the same if it takes that lock. `docs/exclusions.md` records the failure
  mode.
- **Upstream:** [#80993](https://tracker.ceph.com/issues/80993). Upstream QA
  logged the failure in 2022 as
  [#56575](https://tracker.ceph.com/issues/56575), which was resolved by a
  test-only change (d3457c64b1b).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-011); verified 2026-09-27.

## cls_otp divides by a stored step_size that is never validated

- **Kind:** defect, unfixed through main.
- **Evidence:** `otp_instance`'s verify path computes
  `index = result + (secs - time_ofs) / step_size` (v19.2.6
  `cls_otp.cc:143`, main `:139`), where `step_size` is a `uint32_t` a caller
  can store as 0. `otp_set` writes the decoded value with no check
  (`cls_otp.cc:301-347`). liboath does not stop the division: given a zero
  time step, `oath_totp_validate4_callback` substitutes 30
  (`liboath/totp.c:542-543`) and validates a correct code, so a non-negative
  result reaches the division and the OSD faults on the integer divide by
  zero. `trim_expired()` also reads `step_size`, for its replay window
  (`cls_otp.cc:100` at v19.2.6 and v20.2.4, main `:96`).
- **Trigger:** privileged only. Storing `step_size == 0` needs direct RADOS
  write caps on the OTP pool (`<zone>.rgw.otp`, `svc_otp.cc:22-24`), or the
  RGW admin cap `metadata=write` via `metadata put otp:<uid>`, whose
  `otp_info_t::decode_json` reads `step_size` with no `> 0` guard
  (`cls_otp_types.cc:69`). `radosgw-admin mfa create` defaults it to 30
  (`rgw_admin.cc:10824-10825`), and no S3 end-user path stores an OTP entry.
  So the effect is a denial of service (an OSD SIGFPE that recurs on each
  check until the entry is cleared) reachable only by a cluster-service or
  admin actor, not a cross-privilege escalation.
- **Releases:** every release checked, v19.2.2 through main.
- **rgw-go:** no cls_otp client yet (phase 2, MFA). When it writes OTP
  entries it must reject a zero `step_size`, and it must not divide by a
  stored `step_size` without guarding it.
- **Upstream:** [#80948](https://tracker.ceph.com/issues/80948). Its fix,
  [ceph/ceph#72250](https://github.com/ceph/ceph/pull/72250), a draft, adds a
  file-local `effective_step_size()` that uses liboath's default time step
  (`OATH_TOTP_DEFAULT_TIME_STEP_SIZE`) for a stored 0, at the divide and in
  `trim_expired()`'s replay window, so it also guards entries already stored
  with 0.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-012); liboath premise and
  trigger verified 2026-09-27; confirmed in the source at v19.2.6, v20.2.4
  and main. A userspace reproducer that calls liboath alone, on
  [#80948's note-1](https://tracker.ceph.com/issues/80948#note-1), fails
  with SIGFPE (exit status 136) for a `step_size` of 0 and runs normally for
  30. No run on a live OSD was attempted: it would be a crash reproduction.

## cls_otp computes the replay index from an unsigned window distance

- **Kind:** defect, unfixed through main.
- **Evidence:** verify calls `oath_totp_validate2(..., window, nullptr, otp)`
  (v19.2.6 `cls_otp.cc:133-136`), discarding the signed position
  out-parameter, and sets the replay index to the return value plus the
  current step (`:143`), guarding with `if (index <= last_success)` (`:145`)
  and storing `last_success = index` (`:150`). liboath's return value is the
  absolute window distance in both directions; the sign is carried only by
  the discarded `otp_pos` (`liboath/totp.c:547-596`, and the documented
  contract "Returns absolute value of position in OTP window"). A code
  matched `iter` steps in the past therefore records an index `2*iter` steps
  ahead of the true step, so replay handling misfires: legitimate codes for
  the intervening steps are refused, and an earlier code can be accepted
  after a later one within the window. No code outside the window is
  accepted, so it does not bypass MFA.
- **Releases:** every release checked, v19.2.2 through main.
- **rgw-go:** no cls_otp client yet (phase 2, MFA). If it reproduces the
  replay index it must take the direction from the position out-parameter,
  not from the return value.
- **Upstream:** [#80949](https://tracker.ceph.com/issues/80949), which we
  filed. Its fix, [ceph/ceph#72253](https://github.com/ceph/ceph/pull/72253),
  a draft, passes `&otp_pos` and sets the index to
  `(secs - otp.time_ofs) / otp.step_size + otp_pos`. It changes the same
  statement as [ceph/ceph#72250](https://github.com/ceph/ceph/pull/72250),
  #80948's fix; the two are independent, and whichever merges second needs a
  one-line rebase.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-013); liboath premise
  verified 2026-09-27 and confirmed at oath-toolkit-2.6.11, whose
  `liboath/totp.c` returns the absolute position and carries its sign only in
  `otp_pos`. A userspace reproducer that calls liboath alone, run in a CentOS
  Stream 9 container and re-run independently, shows the defect and the fix
  side by side; it is attached to #80949. Not reproduced on a running
  cluster.

## librbd leaks the update-watch context when registration fails

- **Kind:** defect, currently unreachable.
- **Evidence:** at v19.2.6 `rbd_update_watch` allocates a `C_UpdateWatchCB`
  and returns it as the handle whatever `register_update_watcher` returns
  (`librbd.cc:6887-6897`). Only `rbd_update_unwatch` frees it, so a failed
  registration would leak it. `register_update_watcher` returns 0 on every
  path from v19.2.3 through v20.3.0, so the failure cannot happen today.
- **Releases:** every release checked, v19.2.3 through v20.3.0.
- **rgw-go:** unaffected; it does not use rbd. go-ceph's side of the same
  path is ceph/go-ceph#1342.
- **Upstream:** not filed; the leak is unreachable today. go-ceph's side is
  [ceph/go-ceph#1342](https://github.com/ceph/go-ceph/pull/1342).
- **Found:** go-ceph upstreaming audit, 2026-09-27.

## The monitor's default for insecure key creation lags auth_allowed_ciphers

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 the default of `mon_auth_allow_insecure_key` is
  derived from the monmap's `auth_allowed_ciphers` only in the AuthMonitor's
  `check_health` (`AuthMonitor.cc:482` and `:506-508`), which runs from the
  leader's tick (`:172-184`, every `mon_tick_interval`, 5 s) and when it
  encodes an auth proposal (`:479`). So an insecure key type is refused
  (`:1534-1537`) right after `ceph mon set auth_allowed_ciphers` allows it,
  which is a monmap change, and on a newly elected leader, until the next
  tick. The option's description says the default follows the allowed
  ciphers (`mon.yaml.in:769-781`). The same at v20.2.4 and main. Derived from
  the source; not observed on a cluster.
- **Releases:** v19.2.6, v20.2.4, v21.1.1 and main; earlier releases lack the
  key-type switch.
- **rgw-go:** unaffected; it sends no auth commands. Its only monitor command
  is `osd dump`.
- **Upstream:** [#80997](https://tracker.ceph.com/issues/80997).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-015); verified 2026-09-27.

## The monitor reports a refused cephx key type as EINVAL

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 `get_cipher_type` answers EINVAL for an unknown
  key type, ENOTSUP for AES256KRB5 without the monitor feature, and EPERM for
  a type that policy refuses (`AuthMonitor.cc:1510-1547`). Each of its five
  callers, `auth add`, the pending-key commands, `auth get-or-create[-key]`,
  `fs authorize` and `auth rotate`, replaces that code with EINVAL
  (`:1644-1646`, `:1740-1742`, `:1824-1826`, `:1907-1909` and
  `:2070-2072`), so only the status text tells a refusal from a bad
  argument. The same at v20.2.4 and main.
- **Releases:** v19.2.6, v20.2.4, v21.1.1 and main; earlier releases lack the
  key-type switch.
- **rgw-go:** unaffected; it sends no auth commands.
- **Upstream:** [#80998](https://tracker.ceph.com/issues/80998).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-016); verified 2026-09-27.

## The --name error lists the entity types as raw bytes

- **Kind:** defect, a regression new in v19.2.6 and v20.2.4; fixed on the
  squid and tentacle branches, in no release yet.
- **Evidence:**
  - `EntityName::get_valid_types_as_str` streams
    `STR_TO_ENTITY_TYPE[i].first`, an `entity_type_t`, which is a `uint8_t`
    (`entity_name.cc:145-155`, `msgr.h:89`, identical at v19.2.6 and
    v20.2.4), so it writes each type's byte instead of its name. Its one
    caller is the `-n`/`--name` error of `ceph_argparse_early_args`
    (`ceph_argparse.cc:528`; v20.2.4 `:532`), which `global_pre_init` runs
    for radosgw and every other program built on `global_init`.
  - Reproduced on the release images without a cluster, since early
    arguments are parsed before any configuration is read:
    `podman run --rm --network=none quay.io/ceph/ceph:v19.2.6 radosgw
    --name=bogus.x`, and the same on `v20.2.4`, both exit 1 with identical
    output, shown here through `cat -v`: `error parsing 'bogus.x': expected
    string of the form TYPE.ID, valid types are:  , ^A, ^D, ^B, ^P, ^H`. The
    list is the bytes 0x20, 0x01, 0x04, 0x02, 0x10 and 0x08, which are auth,
    mon, osd, mds, mgr and client. The v21.1.0 image prints `valid types are:
    auth, mon, osd, mds, mgr, client`.
  - The regression is main's ad8e5eb399a, "common/entity_name: cleanup
    entity_name::type", which made the table (type, name) pairs and changed
    the stream from `.str` to `.first`. It reached the floor releases as
    e03a5724798 on squid and f92ac6f3e6a on tentacle; v19.2.5, v20.2.3 and
    v21.1.0 print the names. Ceph commit 18e32faf20a, "common/entity_name:
    print type names in get_valid_types_as_str()", streams `.second`. Its
    `Fixes:` line names 0753b1d5326, which only adds a `type_str` field to
    `EntityName::dump`.
- **Releases:** v19.2.6 and v20.2.4, and main between ad8e5eb399a and
  18e32faf20a.
- **rgw-go:** unaffected. `cephconf.ParseEarly` rejects the same names and
  prints the type names: `valid types are: auth, mon, osd, mds, mgr,
  client`.
- **Upstream:** [#79678](https://tracker.ceph.com/issues/79678), resolved,
  lists "EntityName valid-types garbage" among the cephx merge's make-check
  fallout, fixed on main by
  [ceph/ceph#71165](https://github.com/ceph/ceph/pull/71165). Its squid
  backport is [#79640](https://tracker.ceph.com/issues/79640) with
  [ceph/ceph#71190](https://github.com/ceph/ceph/pull/71190), and its
  tentacle backport [#79638](https://tracker.ceph.com/issues/79638) with
  [ceph/ceph#71191](https://github.com/ceph/ceph/pull/71191); both backport
  trackers also carry the MonClient.h build break,
  [#79628](https://tracker.ceph.com/issues/79628), and bear its title. The
  three pull requests merged between 2026-08-19 and 2026-08-21. Neither
  backport tracker names a release: "Released In" is empty on both, and
  "Fixed In" is v19.2.6-238-g5e2e9bc161 and v20.2.4-36-g7364075cb8, commits
  past the floor tags, so no released Squid or Tentacle version carries the
  fix.
- **Found:** phase 1 unit G, early-argument parsing, 2026-09-29; reproduced
  2026-09-29 on the v19.2.6 and v20.2.4 images.

## radosgw ignores a bad port in an IPv6 endpoint

- **Kind:** defect, unfixed through main, with a fix in review. Unreproduced
  on a running radosgw; C++ reproducers of the function show it.
- **Evidence:**
  - For an IPv6 `endpoint` or `ssl_endpoint`, `parse_endpoint` parses the
    port with `parse_port` and then the address with `make_address_v6`,
    passing both the same error code and checking it only after the
    second (`rgw_asio_frontend.cc:574` then `:580` at v19.2.6, `:526` then
    `:532` at v20.2.4, `:542` then `:548` on main at 7ed73efc1be). The IPv4
    branch returns on the port's error first (`:585-588` at v19.2.6).
  - boost asio's `socket_ops::inet_pton`, which `make_address_v6` calls,
    clears the last error on entry and after the system `inet_pton` sets
    the error code from errno, so a successful address parse resets the
    port's error (boost 1.82 `socket_ops.ipp:2537` and `:2765-2766`, 1.87
    `:2604` and `:2832-2833`; the tags build 1.82 and 1.87,
    `CMakeLists.txt:709` and `:723` at v19.2.6, `:754` and `:768` at
    v20.2.4).
  - `parse_port` returns what `strtoul` read, truncated to 16 bits, even
    when it sets the error (`:537-547` at v19.2.6). So `endpoint=[::1]:http`
    listens on port 0, an ephemeral port, and `endpoint=[::1]:70000` on port
    4464, where `endpoint=127.0.0.1:http` and `endpoint=127.0.0.1:70000`
    are refused.
  - A reproducer compiles `parse_port` and `parse_endpoint`, copied
    verbatim from v20.2.4 (`:489-548`, identical to v19.2.6 `:537-596`),
    against boost 1.83 and prints `[::1]:http -> ec=ok port=0`,
    `[::1]:70000 -> ec=ok port=4464`, `127.0.0.1:http -> ec=Invalid
    argument port=0` and `127.0.0.1:70000 -> ec=Numerical result out of
    range port=4464`.
- **Releases:** v13.2.3 and later on mimic, and v14.1.0 and later, through
  v19.2.6, v20.2.4 and main. The IPv6 branch came with a3b4124fd25, "rgw:
  beast frontend parses ipv6 addrs"
  ([ceph/ceph#24887](https://github.com/ceph/ceph/pull/24887), the fix for
  [#36662](https://tracker.ceph.com/issues/36662)), and its mimic
  backport, and has never checked the port's error.
- **rgw-go:** refuses an IPv6 endpoint whose port does not parse, as
  radosgw refuses an IPv4 one (`parseEndpoint`,
  `internal/frontend/spec.go`); `docs/exclusions.md` records the
  difference.
- **Upstream:** we filed [#81305](https://tracker.ceph.com/issues/81305).
  The fix in review is
  [ceph/ceph#72305](https://github.com/ceph/ceph/pull/72305) (draft), which
  makes the IPv6 branch return on the port's error, as the IPv4 branch does.
  No earlier tracker issue or pull request reports it. Searched 2026-09-29,
  each search first run on a known match:
  - the tracker's full text, all projects, for `parse_endpoint`,
    `parse_port` and `"endpoint=["` (known match: #36662's description);
  - ceph/ceph pull requests by keyword for `parse_endpoint` (known match:
    #24887), which finds only
    [ceph/ceph#66290](https://github.com/ceph/ceph/pull/66290) and
    [ceph/ceph#27008](https://github.com/ceph/ceph/pull/27008), neither of
    which changes the function;
  - every open ceph/ceph pull request, and every `rgw` pull request closed
    since 2025-10-01, by whether it changes `src/rgw/rgw_asio_frontend.cc`
    (known match: [ceph/ceph#68920](https://github.com/ceph/ceph/pull/68920)),
    with each such pull request's copy of `parse_endpoint` checked. None
    changes it; the ssl hot-reload pull requests only move its caller.
  On main, only 9e5d4bd1a5d (default ports) and ed70d843df4 (spelling)
  have changed the function since the IPv6 branch arrived.
- **Found:** phase 1 frontend work (unit G), 2026-09-29; the reproducer
  was run in review. The rgw-bug-reproduction session confirmed it on
  2026-10-01 with its own reproducer, the two functions copied verbatim
  against a real boost::asio: `[::1]:notaport` gives port 0 and
  `[::1]:99999` port 34463, both with no error, while
  `127.0.0.1:notaport` is refused. Not reproduced on a running radosgw.

## Squid's radosgw fails to start when its realm search meets a realm whose period cannot be read

- **Kind:** defect, fixed on main and tentacle, unfixed on squid.
  Unreproduced: found by reading the code at v19.2.6.
- **Evidence:** at v19.2.6, when the zone is found but its realm's current
  period does not hold it, `RGWSI_Zone::do_start` searches every realm for
  the zone (`search_realm_with_zone`, `services/svc_zone.cc:178-192`). The
  search skips a realm it cannot open (`:105-109`) but returns the error of
  `RGWRealm::find_zone` (`:111-116`), which returns its period's init error
  although it logs "period init failed ... skipping"
  (`rgw_realm.cc:216-242`), and `do_start` then fails (`svc_zone.cc:189-191`).
  A realm with an empty current period is one such realm: `RGWPeriod::init`
  reads `periods..latest_epoch` for it and gets ENOENT
  (`rgw_period.cc:14-46`). The search runs for an empty current period too,
  so a realm without a readable period stops radosgw from starting,
  although SiteConfig::load, which runs first, falls back to the local
  zonegroup for it (`driver/rados/rgw_zone.cc:1239-1250`). radosgw-admin
  writes an initial period with every realm it creates
  (`driver/rados/rgw_zone.cc:513-522`), so the shape needs a period removed
  or a realm written outside it.
- **Releases:** squid. v20.2.4 has no realm search: `RGWSI_Zone::do_start`
  takes the zonegroup and zone from SiteConfig
  (`services/svc_zone.cc:98-103` at v20.2.4).
- **rgw-go:** searches no realm. Without a readable period it takes the
  local zonegroup and starts, as Tentacle does (`docs/exclusions.md`,
  "Zone and placement differences").
- **Upstream:** fixed on main by
  [ceph/ceph#63266](https://github.com/ceph/ceph/pull/63266) for
  [#71291](https://tracker.ceph.com/issues/71291), which reports another
  failure of the same search, "incorrect zonegroup" with two realms: it
  removes `search_realm_with_zone` with the rest of `do_start`'s duplicated
  startup logic. Backported to tentacle by
  [ceph/ceph#66300](https://github.com/ceph/ceph/pull/66300)
  ([#73890](https://tracker.ceph.com/issues/73890)) before v20.2.4. No
  squid backport: #71291's only copy is tentacle's, and origin/squid
  (a742f50616e, 2026-09-03) still calls the search. No issue reports this
  failure (searched 2026-09-30); each search was first run on a known
  match.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds
    [#80948](https://tracker.ceph.com/issues/80948) by
    `oath_totp_validate4_callback`, a word only in its description. Terms:
    `search_realm_with_zone` (only
    [#56313](https://tracker.ceph.com/issues/56313), a crash while listing
    realms), `search_realm_conf`, `RGWRealm::find_zone`, `realm.find_zone`
    and `can't open realm skipping` (none), `period init failed skipping`
    (four unrelated issues) and `searching for the correct realm` (three
    test logs).
  - ceph/ceph pull requests through `gh search prs`, which finds #63266
    and #66300 by "remove duplicated startup logic". Terms:
    `search_realm_with_zone` (#63266 and the configstore refactor #62398),
    `period init failed` (#62398), `find_zone` (closed zipper work in
    progress only) and `RGWRealm find_zone` (none).
- **Found:** phase 1 metadata work (unit M), 2026-09-29, reading
  `RGWSI_Zone::do_start` for rgw-go's zone resolution.

## radosgw-admin bucket rm exits 0 when it removes nothing

- **Kind:** defect.
- **Evidence:**
  - `radosgw-admin bucket rm` calls `RGWBucketAdminOp::remove_bucket` and
    discards what it returns (`rgw_admin.cc:8768-8778` at v19.2.6,
    `radosgw-admin/radosgw-admin.cc:9226-9237` at v20.2.4).
    `remove_bucket` returns the error of finding the bucket and of removing
    it (`driver/rados/rgw_bucket.cc:1289-1308` at v19.2.6; the lookup is at
    `:1451-1455` at v20.2.4), so the command exits 0 whether or not it
    removed the bucket.
  - Reproduced on the rooket clusters on 2026-09-29, with the site options.
    On rgw-go-squid (19.2.6), `bucket rm --bucket s3t-leftover-tenant
    --purge-objects`, for a bucket that exists only as
    `s3tt/s3t-leftover-tenant`, printed nothing and exited 0, and the
    bucket stayed listed. On rgw-go-tentacle (20.2.4), `bucket rm --bucket
    no-such-bucket-rgw-go --purge-objects` exited 0, while `bucket stats`
    on the same name failed with `(2002)` and exit status 2.
- **Releases:** v19.2.6, v20.2.4 and main (7ed73efc1be, 2026-09-25,
  `radosgw-admin/radosgw-admin.cc:9853-9864`).
- **rgw-go:** unaffected as a gateway: the tool is Ceph's. The s3-tests
  harness removes buckets with it, so `hack/s3tests/run.sh` lists the
  user's buckets again afterwards and stops when one is left, and it names
  a tenant's bucket `tenant/name`, the form `bucket rm` finds.
- **Upstream:** no issue reports it (searched 2026-09-30); each search
  method was first run on a known match. Upstream main's own exit-code
  tests record the behaviour without calling it a bug:
  `src/test/rgw/radosgw-admin/test-bucket-exit-codes.sh`, added in
  ad8b642adad (2026-09-15, in no release), says "rm ignores the op's return
  value, so missing --bucket silently exits 0" (`:835`) and expects exit 0
  from `bucket rm --bucket=no-such-bucket --purge-objects` (`:222`), while
  it marks 42 known bugs of other commands `XFAIL`. The nearest report is
  [#80577](https://tracker.ceph.com/issues/80577), Fix Under Review, on the
  exit codes of `bucket stats`, `bucket list`, `bucket reshard` and
  `bucket set-min-shards`; its fix,
  [ceph/ceph#71858](https://github.com/ceph/ceph/pull/71858) (open), leaves
  `bucket rm` as it is.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds
    [#79678](https://tracker.ceph.com/issues/79678) by `valid-types
    garbage`. Terms: `bucket rm exit code`, `bucket rm return code`,
    `bucket rm exit status`, `bucket rm nonexistent`, `bucket rm
    non-existent`, `bucket rm does not exist`, `bucket rm silently`,
    `remove_bucket return value`, `radosgw-admin exit code 0` and
    `radosgw-admin returns 0 error`: #80577 and unrelated issues only.
  - ceph/ceph pull requests through `gh search prs`, which finds #71165 by
    `get_valid_types_as_str`. Terms: `BUCKET_RM` (none) and `80577` (#71858).
    The multi-word terms were rerun on 2026-09-30 with each word its own
    argument and `bucket rm` a phrase, a method that finds
    [ceph/ceph#59609](https://github.com/ceph/ceph/pull/59609) by `guard against
    dir suggest during reshard`: `"bucket rm" radosgw-admin`, `remove_bucket
    radosgw-admin return`, `radosgw-admin "bucket rm" exit` and `bucket rm
    purge-objects` find bypass-gc and multisite changes to the removal and the
    2019 fixes for a `--purge-objects` hang; `radosgw-admin bucket commands
    return codes`, `radosgw-admin return code` and `radosgw-admin exit code`
    find #71858, [ceph/ceph#71155](https://github.com/ceph/ceph/pull/71155)
    (exit-code tests for the command-line handling) and the open radosgw-admin
    refactors [ceph/ceph#69145](https://github.com/ceph/ceph/pull/69145) and
    [ceph/ceph#71462](https://github.com/ceph/ceph/pull/71462), whose `bucket
    rm` still discards what `remove_bucket` returns. None changes `bucket rm`'s
    exit status.
- **Found:** phase 1 unit T, the s3-tests harness's bucket purge,
  2026-09-29.

## radosgw's S3 ListBuckets reads neither max-buckets nor continuation-token

- **Kind:** defect, fixed on main after v20.2.4. Unreproduced on a running
  radosgw; found by reading the source.
- **Evidence:**
  - `RGWListBuckets_ObjStore_S3::get_params` sets `limit = -1` and reads
    no query parameter (`rgw_rest_s3.h:133-136` at v19.2.6, `:134-137` at
    v20.2.4), so `max-buckets`, `continuation-token` and `prefix` are
    ignored and every ListBuckets returns all of the owner's buckets.
  - `send_response_begin`, `send_response_data` and `send_response_end`
    write the owner and the buckets and no `ContinuationToken` or `Prefix`
    element (`rgw_rest_s3.cc:1511-1547` at v19.2.6, `:1622-1658` at
    v20.2.4).
  - A client that pages with `max-buckets` gets every bucket in its first
    response and no token to continue from.
- **Releases:** v19.2.6 and v20.2.4. On main, e0d797da790, merged as
  c531eec199d, reads `max-buckets` and `continuation-token` and writes
  `ContinuationToken`; it leaves `prefix` unread.
- **rgw-go:** reproduces it on both releases: its ListBuckets reads none of
  the three parameters and writes neither element (`listBuckets`,
  `internal/s3/listbuckets.go`). Revisit when a floor release carries the
  fix.
- **Upstream:** [#72315](https://tracker.ceph.com/issues/72315), "support
  paginated s3 ListBuckets", resolved on main by
  [ceph/ceph#64742](https://github.com/ceph/ceph/pull/64742); no squid or
  tentacle backport issue or pull request exists. `prefix` is
  [#75463](https://tracker.ceph.com/issues/75463), open, with
  [ceph/ceph#67920](https://github.com/ceph/ceph/pull/67920). Searched
  2026-09-30; each search was first run on a known match:
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds #72315 by
    `continuation-token`, a word only in its description. Terms:
    `max-buckets`, `continuation-token` and `ListBuckets` (#72315, #75463,
    and the resolved #57901 and its backports for the 1000-bucket
    truncation; nothing else about pagination).
  - ceph/ceph pull requests through `gh search prs`, which finds #64742 by
    "paginated ListBuckets". Terms: `paginated ListBuckets` (#64742 only)
    and `ListBuckets` in titles (#64742 and the truncation fix
    [ceph/ceph#48559](https://github.com/ceph/ceph/pull/48559) with its
    backports).
- **Found:** phase 1 gateway work (unit G), 2026-09-30, reading
  `RGWListBuckets_ObjStore_S3` for rgw-go's ListBuckets.

## url_decode reads outside its hex table for a byte above 0x7f after "%"

- **Kind:** defect, unfixed through main, with a fix in review. Reproduced
  by calling the `url_decode` that the librgw2 19.2.6 and 20.2.4 packages
  ship, and under AddressSanitizer; not reproduced on a running radosgw.
- **Evidence:**
  - `hex_to_num` looks an escape's digits up in `HexTable`'s 256-byte
    table with the `char` cast to `int` (`rgw_common.cc:1690-1692` at
    v19.2.6, `:1753-1755` at v20.2.4, `:1779-1781` on main at
    a956c21a8c9). ceph compiles every build with `-fsigned-char`
    (`src/CMakeLists.txt:95` at v19.2.6, `:102` at v20.2.4), so on every
    architecture a byte from 0x80 to 0xff indexes from -128 to -1 and
    reads the 128 bytes before the table.
    `url_decode` takes a negative value as a bad digit and empties its
    result, and uses any other as the digit (`:1701-1734` at v19.2.6,
    `:1764-1797` at v20.2.4).
  - A program `dlopen`s `librgw.so.2` from librgw2-19.2.6-0.el9 and
    librgw2-20.2.4-0.el9 and calls the exported `url_decode` on `%`, each
    byte from 0x80 to 0xff, and `0`. In 19.2.6, 104 of the 128 bytes read
    as the digit 0, 21 as other digits and 3 (0x90, 0x98, 0xe0) as none;
    in 20.2.4, 102 as 0, 17 as others and 9 as none. `%\xc3\xbc` decodes
    to a NUL byte in both. What the read finds depends on the build.
  - A reproducer that copies `HexTable`, its lookup and `url_decode`
    verbatim, built with `-fsigned-char -fsanitize=address`, reports a
    global-buffer-overflow read in the lookup for `%` followed by any byte
    above 0x7f (index -128 for 0x80), and runs clean with the fix below.
- **Releases:** v19.2.6, v20.2.4 and main (a956c21a8c9, 2026-09-30).
- **rgw-go:** takes such a byte as no hex digit, so the result is empty,
  as for any other bad digit; `docs/exclusions.md` records the
  difference.
- **Upstream:** we filed [#81267](https://tracker.ceph.com/issues/81267),
  which cites #4755, rather than comment on a resolved issue from 2013. The
  fix in review is [ceph/ceph#72272](https://github.com/ceph/ceph/pull/72272)
  (draft), a one-line change that indexes the table with the byte as
  `unsigned char`, so a byte above 0x7f finds the table's in-bounds -1 and
  is refused as a bad digit. Not found by us: #4755 reported it first.
  [#4755](https://tracker.ceph.com/issues/4755) (2013, Resolved), on
  `url_decode` assuming a signed `char` on armhf, remarked in passing that
  `hex_to_num` "has an obvious related bug in places where char is _not_
  unsigned"; the lookup it names still reads outside the table. Searched
  2026-09-30, each search first run on a known match:
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds
    [#81000](https://tracker.ceph.com/issues/81000) by `dir suggest during
    reshard`. Terms: `url_decode` (#4755 and reports on `+` and on policy
    decoding), `hex_to_num` (#4755,
    [#6672](https://tracker.ceph.com/issues/6672) on the table's
    initialization race, and #8702 on `+`), `HexTable` (none),
    `percent-encoded`, `bad escape` and `escape sequence` (unrelated
    issues only).
  - ceph/ceph pull requests through `gh search prs`, which finds
    [ceph/ceph#59609](https://github.com/ceph/ceph/pull/59609) by `guard
    against dir suggest during reshard`. Terms: `hex_to_num`
    ([ceph/ceph#783](https://github.com/ceph/ceph/pull/783), the
    initialization race), `HexTable`, `invalid url-encoding` and `rgw
    signed char` (none), and `url_decode` (23 pull requests; none changes
    `hex_to_num`).
- **Found:** phase 1 auth work (unit A), transcribing `url_decode`,
  2026-09-29; the shipped libraries were called the same day. The
  rgw-bug-reproduction session reproduced it under AddressSanitizer on
  2026-09-30, and attached the reproducer to the tracker issue.

## A malformed percent-escape in the path makes radosgw serve another path

- **Kind:** defect, unfixed through main, with a fix in review. Reproduced
  on a running radosgw at v19.2.6 and v20.2.4.
- **Evidence:**
  - `RGWREST::preprocess` decodes the path with `url_decode` and checks
    the result only for a NUL (`rgw_rest.cc:2182-2186` at v19.2.6,
    `:2204-2208` at v20.2.4). `url_decode` returns an empty string for an
    escape with a character that is no hex digit, such as `%zz`, and stops
    at a `%` with fewer than two characters after it
    (`rgw_common.cc:1725-1726` and `:1718-1719` at v19.2.6, `:1788-1789`
    and `:1781-1782` at v20.2.4), so `/b/k%` and `/b/k%4` decode as
    `/b/k`. A virtual-hosted request's bucket is put in front of the path
    before the decode (`rgw_rest.cc:2154-2161` at v19.2.6), so it is
    emptied with the rest.
  - The S3 handler reads an empty path as naming neither bucket nor
    object (`init_from_header`, `rgw_rest_s3.cc:4908-4914` at v19.2.6,
    `:5468-5474` at v20.2.4). So `GET /b/%zz` is served as `GET /`, a
    ListBuckets, and a request for the key `k%` acts on the key `k`.
  - SigV4 signs the same decode: the canonical URI is `aws4_uri_recode`
    of the path, and `/` when that is empty (`get_v4_canonical_uri`,
    `rgw_auth_s3.h:598-612` at v19.2.6, `:601-615` at v20.2.4), so the
    signature such a request needs is one over the path radosgw serves.
- **Releases:** v19.2.6, v20.2.4 and main (`url_decode` still returns an
  empty string, `rgw_common.cc:1815` at a956c21a8c9).
- **rgw-go:** net/http answers such a request `400 Bad Request` before any
  handler runs; `docs/exclusions.md` records the difference under
  "Frontend differences".
- **Upstream:** we filed [#81301](https://tracker.ceph.com/issues/81301),
  which cites #71458 and ceph/ceph#63521, rather than comment on the
  resolved CVE. The fix in review is
  [ceph/ceph#72301](https://github.com/ceph/ceph/pull/72301) (draft): a
  strict `url_decode` that reports a malformed escape, which the path's
  routing callers refuse with 400, plus unit tests. The lenient `url_decode`
  stays as it is, so #71458's empty copy-source check still holds. Not found
  by us: #71458 reported the same `url_decode` result first, through
  another caller.
  [#71458](https://tracker.ceph.com/issues/71458) (CVE-2025-48052,
  Resolved) was the same empty result crashing UploadPartCopy through
  `x-amz-copy-source`; its fix,
  [ceph/ceph#63521](https://github.com/ceph/ceph/pull/63521), guards that
  caller in `rgw_op.cc` and changes `url_decode` only by a blank line.
  Searched 2026-09-30 with the methods and known matches of the entry
  above: tracker terms `url encoding`, `invalid url`, `malformed url`,
  `percent sign` and `bad escape` find #71458, its backports and
  unrelated issues; pull request terms `url_decode` and `invalid
  url-encoding` find no fix for the path.
- **Found:** phase 1 auth work (unit A), checking the frontend's
  percent-escape entry against `url_decode`, 2026-09-30. The
  rgw-bug-reproduction session reproduced it on running v19.2.6 and v20.2.4
  clusters the same day. `GET /repro/k%` returns the body of key `k`, while
  `GET /repro/k%25` answers 404. `GET /repro/%zz` returns a ListAllMyBuckets
  result for a URL that names an object in bucket `repro`. The radosgw log
  shows the routing on the decoded path. The reproducer, written in Go, its
  output and the log are attached to the tracker issue.

## radosgw skips or never finishes every bucket-index batch on a zero rgw_bucket_index_max_aio

- **Kind:** defect, unfixed through main, with a fix in review.
- **Evidence:** `rgw_bucket_index_max_aio`, the number of bucket-index
  operations radosgw keeps in flight across a bucket's shards, is a `uint`
  with no `min:` (`common/options/rgw.yaml.in:195-202` at v19.2.6,
  `:201-208` at v20.2.4), so a configured 0 is accepted.
  - v19.2.6: `CLSRGWConcurrentIO::operator()` issues shard operations while
    `max_aio-- > 0` (`cls/rgw/cls_rgw_client.cc:28`), so a zero issues none.
    `BucketIndexAioManager::wait_for_completions` then returns false because
    nothing is pending (`:139-141`), and the batch returns 0. Bucket-index
    initialization on bucket creation (`services/svc_bi_rados.cc:369-371`)
    and listing (`driver/rados/rgw_rados.cc:9710-9713`) are among the batches
    that report success without touching a shard.
  - v20.2.4: each batch reads the option into a `size_t` and hands it to
    `rgwrados::shard_io` as its window, as the index-header read does
    (`services/svc_bi_rados.cc:374-391`). Before sending a shard operation,
    the reader and both writers wait for a completion while
    `outstanding.size() >= max_concurrent` (`driver/rados/shard_io.h:353`,
    `:480` and `:603`), which a zero window satisfies with nothing
    outstanding. `async_wait` only parks the caller (`:213-226`), and only a
    shard operation's completion wakes it (`maybe_complete`, `:228-238`), so
    the batch never finishes: a request's coroutine stays suspended, and a
    blocking caller waits forever (`svc_bi_rados.cc:389-390`).
- **Releases:** v19.2.6 skips the batch; v20.2.4 and main (a956c21a8c9,
  2026-09-30, `rgw.yaml.in:232-239`, `shard_io.h:359`, `:486` and `:610`)
  never finish it.
- **rgw-go:** reads the option once at startup and uses a zero as 1, with an
  error-level log line naming the option, so the driver's bucket-index
  fan-out always has a window. `docs/exclusions.md` records the difference.
- **Upstream:** we filed [#81302](https://tracker.ceph.com/issues/81302).
  The fix in review is
  [ceph/ceph#72302](https://github.com/ceph/ceph/pull/72302) (draft), which
  adds `min: 1` to the option, so that `Option::validate` refuses a zero on
  every configuration path. No earlier issue or pull request reports it
  (searched 2026-09-30); each search method was first run on a known match.
  [ceph/ceph#72160](https://github.com/ceph/ceph/pull/72160), the fix for
  [#80991](https://tracker.ceph.com/issues/80991), adds `min: 1` to
  `rgw_lc_max_objs`, `rgw_usage_max_shards` and `rgw_gc_max_objs` only.
  - tracker.ceph.com: the full-text search of every project's issues, open
    and closed, which finds #80991 by `rgw_usage_max_shards`. Terms:
    `rgw_bucket_index_max_aio`, `bucket_index_max_aio`,
    `CLSRGWConcurrentIO` and `max_concurrent shard_io`: no results.
  - ceph/ceph pull requests through `gh search prs`, which finds #72160 by
    `rgw_usage_max_shards`. Term `rgw_bucket_index_max_aio`: six pull
    requests (#28558, #49795, #59199, #59222, #60628 and #61760), none
    about a zero window.
- **Found:** phase 1 unit M, validating the driver's options, 2026-09-30.
  The rgw-bug-reproduction session reproduced it on running clusters the
  same day with `radosgw-admin bucket list --rgw-bucket-index-max-aio=0` on
  a bucket holding eight objects. On v19.2.6 it printed `[]` and exited 0;
  on v20.2.4 it hung until a 30-second timeout ended it (exit 124). The
  output and the patch are attached to the tracker issue.

## radosgw clamps a copy of rgw_override_bucket_index_max_shards that bucket creation never reads

- **Kind:** defect, unfixed through main.
- **Evidence:**
  - `rgw_override_bucket_index_max_shards` is a `uint` with no `max:`
    (`common/options/rgw.yaml.in:177-193` at v19.2.6, `:183-199` at
    v20.2.4).
  - `RGWRados::init_complete` copies it, or the zone's
    `bucket_index_max_shards` when it is 0, into the member
    `bucket_index_max_shards`, lowers the copy to `get_max_bucket_shards()`,
    65521, and logs "bucket index max shards is too large, reset to value:
    65521" (`driver/rados/rgw_rados.cc:1334-1341` at v19.2.6, `:1323-1330`
    at v20.2.4; `services/svc_bi_rados.h:33` and `:94-96` at v19.2.6, `:34`
    and `:95-97` at v20.2.4). Nothing else in `src/rgw` uses that member,
    declared at `rgw_rados.h:401` (v19.2.6) and `:413` (v20.2.4).
  - Bucket creation reads the option itself: `init_default_bucket_layout`
    gives the new index that many shards when it is above 0, and the zone's
    `bucket_index_max_shards` otherwise, neither clamped
    (`driver/rados/rgw_bucket.cc:2790-2796` at v19.2.6; `:2905-2914` at
    v20.2.4, where a shard count the caller passes comes first, also
    unclamped).
  - Above 7877 shards, `rgw_shards_mod` reduces the key's hash modulo 65521
    before the shard count (`driver/rados/rgw_tools.h:46-55` at v19.2.6,
    `:63-72` at v20.2.4), so a bucket created with more than 65521 shards
    never places an entry on a shard above 65520: 65522 shards leave 1
    empty, 70000 leave 4479. `radosgw-admin bucket reshard` refuses more
    than 65521 shards (`rgw_admin.cc:3027-3030` at v19.2.6,
    `radosgw-admin/radosgw-admin.cc:3224-3227` at v20.2.4), and dynamic
    resharding caps its target there (`rgw_rados.cc:10591` at v19.2.6,
    `:11521` at v20.2.4); bucket creation does not.
- **Impact:** the log line reports a limit that is not applied, and a bucket
  created while the option is above 65521 has index shards that never hold
  an entry.
- **Releases:** v19.2.6, v20.2.4 and main (7f50f7a552c, 2026-09-30,
  `rgw_rados.cc:1408-1412` and `rgw_bucket.cc:3088-3090`); older releases
  not checked.
- **rgw-go:** reads the option once at startup, without radosgw's clamp
  (`internal/driver/options.go`), and gives a new bucket's index that many
  shards when it is set, unbounded, as `init_default_bucket_layout` does
  (`Store.defaultLayout` in `internal/driver/bucketops.go`).
- **Upstream:** [#70980](https://tracker.ceph.com/issues/70980), "a bucket
  can only effectively use up to 65,521 bucket index shards", reports it. It
  was rejected on 2025-04-18 once `radosgw-admin bucket reshard` was shown to
  refuse 65522 shards; bucket creation through this option was not checked
  there, and resharding looked protected because it clamps its own count
  while the per-bucket layout never reads the clamped `RGWRados` copy. We
  commented on #70980 with a reproducer of the creation path rather than
  file a new issue. The fix,
  [ceph/ceph#72256](https://github.com/ceph/ceph/pull/72256), a draft, clamps
  both creation branches, the option and a count the caller passes, to
  `rgw_shards_max()`, as resharding and `RGWRados` already do; an option
  `max:` would instead reject an existing configuration on upgrade. No other
  issue or pull request reports it (searched 2026-09-30); each search method
  was first run on a known match.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds #80991 by
    `rgw_usage_max_shards`. Terms: `rgw_override_bucket_index_max_shards`
    (fifteen issues, none about its bound), `get_max_bucket_shards` and the
    log line as a quoted phrase (none), and `65521` (#70980; #20934 with its
    backports, a fixed bug that resharded buckets to 65521 shards; and
    unrelated QA reports).
  - ceph/ceph pull requests through `gh search prs`, which finds
    ceph/ceph#72160 by `rgw_usage_max_shards`. Terms:
    `rgw_override_bucket_index_max_shards` (fourteen pull requests),
    `init_default_bucket_layout` (six) and the words of the log line
    (21), none bounding the option at bucket creation, and
    `get_max_bucket_shards` (none).
- **Found:** phase 1 unit M, reading the driver's options, 2026-09-30. A
  userspace reproducer that copies `rgw_shards_mod` verbatim, re-run
  independently, shows the unreachable shards; it is attached to #70980. A
  run on a live cluster was deferred, the source and the arithmetic being
  conclusive.

## radosgw wraps an rgw_cache_expiry_interval above 18446744073 seconds

- **Kind:** defect, unfixed through main.
- **Evidence:**
  - `rgw_cache_expiry_interval` is a `uint` count of seconds with no `max:`,
    default 900, where 0 turns expiry off
    (`common/options/rgw.yaml.in:3324-3336` at v19.2.6, `:3509-3521` at
    v20.2.4).
  - The object cache (`ObjectCache::set_ctx`) and each chained cache
    (`RGWChainedCacheImpl::init`) assign
    `std::chrono::seconds(get_val<uint64_t>("rgw_cache_expiry_interval"))`
    to an expiry of type `ceph::timespan` (`rgw_cache.h:173` and `:210-211`
    at v19.2.6, `:174` and `:211-212` at v20.2.4;
    `services/svc_sys_obj_cache.h:150` and `:174-175` at v19.2.6, `:152`
    and `:176-177` at v20.2.4). `ceph::timespan` counts nanoseconds in a
    `uint64_t` (`common/ceph_time.h:66` and `:74` at v19.2.6, `:63` and
    `:71` at v20.2.4), so the conversion multiplies by 10^9 modulo 2^64: any
    value above 18446744073 s, about 584 years, wraps.
  - A lookup misses once its entry is older than the result
    (`rgw_cache.cc:30-31` at both tags; `svc_sys_obj_cache.h:184-187` at
    v19.2.6, `:186-189` at v20.2.4). Compiled with the same types (g++
    15.3.1, x86-64): 18446744074 s becomes 0.29 s, so an entry expires 0.29 s
    after it is cached; any multiple of 2^55 s, 2^63 s among them, becomes
    0, which the `expiry.count()` guard reads as expiry off, so entries
    never expire;
    2^64-1 s, whose `std::chrono::seconds` count is -1, becomes 2^64 - 10^9
    ns, about 584 years, so it expires nothing.
- **Releases:** v19.2.6, v20.2.4 and main (7f50f7a552c, 2026-09-30,
  `rgw_cache.h:174` and `:211`, `svc_sys_obj_cache.h:152` and `:176`); older
  releases not checked.
- **rgw-go:** reads the option once at startup and holds any value above
  about 9.2e9 s as the longest Duration, about 292 years
  (`internal/driver/options.go`), so it never wraps. The metadata cache
  (`internal/driver/cache.go`) expires its entries after it, and
  `docs/exclusions.md` records the difference ("A cache expiry interval too
  large for radosgw's clock").
- **Upstream:** [#81218](https://tracker.ceph.com/issues/81218), which we
  filed. Its fix, [ceph/ceph#72255](https://github.com/ceph/ceph/pull/72255),
  a draft, gives the option `min: 0` and `max: 17_G` in `rgw.yaml.in`, so
  `Option::validate()` rejects an overflowing value when it is set, for both
  sites. No earlier issue or pull request reports it (searched 2026-09-30);
  each search method was first run on a known match.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds #80991 by
    `rgw_usage_max_shards`. Terms: `rgw_cache_expiry_interval` and
    `cache_expiry_interval` (twelve issues: multisite cache-coherence reports
    and backports, #77531, #77554, #77731, #80389 and #80864 through #80866;
    #24346, entries never refreshed after the interval, fixed by
    [ceph/ceph#22324](https://github.com/ceph/ceph/pull/22324), with its
    backports; and a luminous performance regression), and `expiry miss`
    (thirteen, none about the option). None reports the conversion.
  - ceph/ceph pull requests through `gh search prs`, which finds
    ceph/ceph#72160 by `rgw_usage_max_shards`. Terms:
    `rgw_cache_expiry_interval` (nine pull requests: #22324 with its
    backports, the cache-version changes #69748 and #71143, #46304 and
    release notes) and `cache_expiry_interval` (none). None changes the
    conversion.
- **Found:** phase 1 unit M, reviewing the driver's options, 2026-09-30; the
  conversion was checked by compiling it with radosgw's types. A
  self-contained C++17 reproducer that models `ceph::timespan`, run in a
  container against a control and re-run independently, shows the wrap; it
  is attached to #81218. Not reproduced on a running cluster.

## radosgw spins or stops caching on a negative usage-log or quota interval

- **Kind:** defect, unfixed through main.
- **Evidence:** `rgw_usage_log_tick_interval`, `rgw_bucket_quota_ttl`,
  `rgw_user_quota_bucket_sync_interval`, `rgw_user_quota_sync_interval` and
  `rgw_user_quota_sync_wait_time` are `int` counts of seconds with no `min:`
  (`common/options/rgw.yaml.in:1651`, `:2168`, `:2262`, `:2275` and `:2298`
  at v19.2.6; `:1734`, `:2260`, `:2362`, `:2375` and `:2398` at v20.2.4),
  so a negative value is accepted. What radosgw does with one, the same at
  both tags unless noted:
  - Usage-log tick: each firing of the usage logger's timer flushes and
    re-arms it `rgw_usage_log_tick_interval` seconds later
    (`rgw_log.cc:105-117`). `add_event_after` converts the seconds to a
    signed `signedspan` and adds it to the monotonic clock as an unsigned
    `timespan` (`common/Timer.cc:137-150`; `common/ceph_time.h:467-470` at
    v19.2.6, `:464-467` at v20.2.4), so a negative interval schedules the
    event that far in the past, and the timer thread flushes without pause.
  - Stats-cache TTL: `RGWQuotaCache::set_stats`, which the bucket and owner
    stats caches share, adds the TTL, and half of it, to the current time
    (`rgw_quota.cc:136-139`) through `utime_t`'s `operator+=(double)`, which
    casts the seconds to `__u64` (`include/utime.h:528-535`). C++ leaves
    that cast undefined for a negative value; with GCC on x86-64 it wraps,
    so `expiration` lands that far before now and `get_stats` never returns
    a cached stat, reading it from RADOS on every quota check
    (`rgw_quota.cc:158-164`). Below about -1.79e9 s, the current Unix time,
    the sum wraps below zero instead, and `cap_to_u32_max` pins
    `expiration` at 2^32-1, in 2106, so the cached stat never expires.
  - Bucket-sync interval: at v19.2.6 `BucketsSyncThread` waits
    `std::chrono::seconds(rgw_user_quota_bucket_sync_interval)`
    (`rgw_quota.cc:378-381`), which a negative makes return at once, so the
    thread loops without pause. v20.2.4 converts the value to `uint64_t`,
    multiplies it by a `double` factor and waits at least 1 s
    (`rgw_quota.cc:394-401`); with the default factor of 1 and g++ on
    x86-64, -1 through -1024 wait 1 s, and anything lower converts back to a
    negative wait and spins.
  - Owner-sync interval: `OwnerSyncThread` waits
    `std::chrono::seconds(rgw_user_quota_sync_interval)`
    (`rgw_quota.cc:427-428` at v19.2.6, `:448-449` at v20.2.4), so a
    negative makes it run `sync_all_owners` without pause.
  - Sync wait: `RGWOwnerStatsCache::sync_owner` adds the wait to the owner's
    last full sync (`rgw_quota.cc:642-648` at v19.2.6, `:663-669` at
    v20.2.4), which a negative puts before now, so every owner the pass does
    not skip as idle gets a full sync on every pass.
  A zero spins the tick, the owner-sync thread and v19.2.6's bucket-sync
  thread too; at v20.2.4 and main a zero bucket-sync interval waits 1 s, as
  -1 through -1024 do, up to 180 times as often as the default. A zero TTL
  or sync wait acts as a small negative one does. The conversions were
  checked by compiling them with radosgw's types (g++ 15.3.1, x86-64, at -O2
  and -O0).
- **Releases:** v19.2.6, v20.2.4 and main (7f50f7a552c, 2026-09-30,
  `rgw_log.cc:116`, `rgw_quota.cc:137-138`, `:392-399`, `:447` and `:663`,
  and still no `min:`); older releases not checked.
- **rgw-go:** reads each option once at startup, keeping a negative value
  (`internal/driver/options.go`). Its usage log flushes every second when
  `rgw_usage_log_tick_interval` is not positive, logging an error
  (`internal/driver/usage.go`; `docs/exclusions.md`, "A usage-log tick
  interval that is not positive"). With `rgw_enable_quota_threads` set, its
  quota bucket-sync and owner-sync workers likewise run every second when
  their interval is not positive, logging an error
  (`internal/driver/stats.go`; `docs/exclusions.md`, "Quota sync intervals
  that are not positive"). A TTL or sync wait that is not positive acts as it
  does in radosgw: every quota check reads the stats from RADOS, and every
  owner the pass does not skip as idle gets a full sync on every pass. Only
  a TTL below the current Unix time's negative differs, which rgw-go never
  pins in 2106 (`docs/exclusions.md`, "A quota stats TTL below about
  -1.79e9 seconds").
- **Upstream:** [#81226](https://tracker.ceph.com/issues/81226), which we
  filed. Its fix, [ceph/ceph#72261](https://github.com/ceph/ceph/pull/72261),
  a draft, adds `min: 1` to all five options.
  [#80991](https://tracker.ceph.com/issues/80991) and
  [#48678](https://tracker.ceph.com/issues/48678) are related but not
  duplicates. No earlier issue or pull request reports it (searched
  2026-09-30); each search method was first run on a known match.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds #80991 by
    `rgw_usage_max_shards`. Terms: the five option names, two to five issues
    each (unrelated reports that mention the options, and older quota and
    usage-log bugs), none about a negative value.
  - ceph/ceph pull requests through `gh search prs`, which finds
    ceph/ceph#72160 by `rgw_usage_max_shards`. Terms: the five option names,
    none to six pull requests each (option descriptions in #71303 and
    #13395, and older quota changes), none bounding them.
- **Found:** phase 1 unit M, reviewing the driver's options, 2026-09-30. A
  self-contained C++17 reproducer that compiles the real expressions with
  GCC 11.5 at v19.2.6, v20.2.4 and main confirms each effect; it is attached
  to #81226. Not reproduced on a running cluster.

## radosgw's ARN conditions compare each ARN component with the text after it

- **Kind:** defect, security-relevant: an `ArnLike` or `ArnEquals` Deny
  whose wildcard is not in the first component can fail to apply (triage
  estimate CVSS 5.8-7.5); unfixed through main. It is reachable from a
  policy since [ceph/ceph#62285](https://github.com/ceph/ceph/pull/62285)
  routed ARN conditions here.
- **Evidence:**
  - `match_policy` walks a pattern and an input colon by colon, but takes
    each piece as `input.substr(last_pos_input, cur_pos_input)` and
    `pattern.substr(last_pos_pattern, cur_pos_pattern)`
    (`rgw_common.cc:2178-2179` at v19.2.6, `:2241-2242` at v20.2.4).
    `substr`'s second argument is a length, and it is given the next
    colon's position, so every piece after the first runs past its colon
    into the components after it, cut at a different length in the
    pattern and in the input, and `match_wildcards` compares those.
  - `arn_like` calls it for `ArnEquals` and `ArnLike`, and negated for
    `ArnNotEquals` and `ArnNotLike` (`rgw_iam_policy.cc:845-852` and
    `:996-1001` at v19.2.6, `:864-871` and `:1000-1005` at v20.2.4),
    although its comment says each of the six components is checked
    separately. So neither `arn:aws:sns:*:123456789012:topic` nor
    `arn:aws:sns:us-east-1:*:topic` matches
    `arn:aws:sns:us-east-1:123456789012:topic`, `arn:aws:s3:*:*:bucket`
    does not match `arn:aws:s3:::bucket`, `arn:aws:s3:::bucket/*/x` does
    not match `arn:aws:s3:::bucket/abc/x`, and `x:a\:y` matches `x:a:y`.
    A pattern whose only wildcard ends it, such as
    `arn:aws:s3:::bucket/*`, still compares correctly. Action matching is
    unaffected: every action name has one colon.
  - Verified by compiling radosgw's `match_policy` unchanged in the
    v19.2.6 and v20.2.4 images and running generated cases; not
    reproduced on a running radosgw. Of 116,003 generated cases whose
    pattern and input hold the same number of colons, two or more, 38,485
    answer differently from matching each component on its own; with
    fewer colons, or unequal counts, the two agree on all 483,997.
  - The comparison dates from ff18f84c70f (2016-12-12, "rgw: Added a
    globbing method for AWS Policies"). ARN conditions reach it since
    62c3e5ec69f (2025-03-13, "rgw/iam: add policy evaluation for
    Arn-based Conditions"), and main still has it at dffaf990666.
- **Impact:** a Deny whose ARN pattern has a wildcard component can fail to
  apply. `ArnNotLike` and `ArnNotEquals` negate the same wrong answer, so a
  statement using them can apply where its author meant it not to.
- **Releases:** every release that evaluates ARN conditions: v19.2.3 and
  later squid, every tentacle release from v20.1.0, and v18.2.8 and later
  reef.
- **rgw-go:** does not reproduce it. Its ARN conditions match each
  component on its own with `MatchWildcards` (`policy.MatchPolicy`), and
  `docs/exclusions.md` records the difference.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. No report or fix is known. Searched
  2026-09-30; each search was first run on a known match:
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds the QA runs
    listing ceph/ceph#57907 by `match_wildcards`, a word only in their
    descriptions. Terms: `match_policy`, `arn_like`, `ArnLike`,
    `ArnEquals`, `ArnNotLike`, `MATCH_POLICY_ARN` and `aws:SourceArn`. The
    only ARN-condition issue is
    [#70481](https://tracker.ceph.com/issues/70481), "iam policy parses
    ArnLike/ArnEquals conditions but evaluates them to false", with its
    reef and squid backports #70595 and #70596; its fix routed ARN
    conditions through `match_policy`. Nothing reports the comparison.
  - ceph/ceph pull requests through `gh search prs`, which finds
    ceph/ceph#57907 by `match_wildcards`. Terms: `match_policy`
    (ceph/ceph#53156 and #16491, both on `match_wildcards`, and an
    unrelated logging change), `arn_like` (none) and `ArnLike`
    ([ceph/ceph#62285](https://github.com/ceph/ceph/pull/62285), the
    #70481 fix, and #62284, bucket logging). None fixes it.
- **Found:** phase 1 authorization work (unit Z), 2026-09-29, transcribing
  `match_policy` for rgw-go's policy matching.

## radosgw takes any policy Action starting with a wildcard for every action

- **Kind:** defect, unfixed through main.
- **Evidence:**
  - The policy parser, `ParseState::do_string`, tests only the first
    character of an Action or NotAction string (`*s == '*'`): when it is
    `*`, the statement gets every action (`allValue`), and only other
    strings are matched against the action names (`rgw_iam_policy.cc:632`
    at v19.2.6, `:645` at v20.2.4). So `"*:GetObject"` or `"*Object"` means
    every action, where matching it as a pattern would give only the
    actions it names.
  - A `Principal` or `NotPrincipal` given as a string is tested the same
    way (`:625` and `:627` at v19.2.6, `:638` and `:640` at v20.2.4), so
    `"Principal": "*x"` is `Principal::wildcard()`, every principal,
    anonymous included.
  - Such a policy is accepted as valid. The same file already compares the
    full token in `parse_principal_` (main `:369`).
- **Impact:** an Allow statement whose Action starts with `*` grants every
  action, not only those its pattern names, a privilege escalation; a Deny
  statement denies every action. A Principal that starts with `*` names
  everyone.
- **Releases:** v19.2.6, v20.2.4 and main (7f50f7a552c, 2026-09-30,
  `rgw_iam_policy.cc:784`, `:786` and `:791`); older releases not checked.
- **rgw-go:** reproduces it: `policy.Parse` takes any Action, NotAction,
  Principal or NotPrincipal string starting with `*` as radosgw does, since
  a stored policy must parse as the zone's radosgw parses it.
- **Upstream:** [#81229](https://tracker.ceph.com/issues/81229), which we
  filed. Its fix, [ceph/ceph#72262](https://github.com/ceph/ceph/pull/72262),
  a draft, compares the full token, `std::string_view{s, l} == "*"`, at the
  three sites. No earlier issue or pull request reports it (searched
  2026-09-30); each search method was first run on a known match.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds #80991 by
    `rgw_usage_max_shards`. Terms: `is_valid_action`, `allValue` and `*:Get`
    (none), `wildcard action` (#68040, on removing a deny-all bucket policy,
    and #62292, a Resource wildcard fixed by
    [ceph/ceph#53156](https://github.com/ceph/ceph/pull/53156)) and
    `NotAction` (#73983, NotAction not evaluated, and #68029, a null
    dereference in `rgw_iam_policy.cc`).
  - ceph/ceph pull requests through `gh search prs`, which finds
    ceph/ceph#72160 by `rgw_usage_max_shards`. Terms: `is_valid_action`
    (#46824, spelling fixes), `allValue` (#70533, policy-evaluation
    logging) and `policy action wildcard` (eleven pull requests; the only
    change to action parsing, #20629, stops the action loop at its first
    match). None fixes it.
- **Found:** phase 1 authorization work (unit Z), 2026-09-30, writing
  rgw-go's action matching. A C++17 reproducer of `do_string`'s exact
  predicate, re-run independently, is attached to #81229; no policy was run
  end to end through `rgw::IAM::Policy`, which needs a full build. Not
  reproduced on a running radosgw.

## radosgw reads a tagging body whose root is not Tagging as an empty tag set

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** `rgw_xml.h`, `rgw_xml.cc` and `rgw_tag_s3.cc` are the same
  blobs at v19.2.6 and v20.2.4, so each of their lines below holds at both.
  - PutObjectTagging and PutBucketTagging parse the body, then look its root
    up with `RGWXMLDecoder::decode_xml("Tagging", tagging, &parser)`
    (`rgw_rest_s3.cc:800` and `:891` at v19.2.6, `:882` and `:973` at
    v20.2.4), leaving `mandatory` at its default, false (`rgw_xml.h:156`).
  - When the root is not `Tagging`, `decode_xml` finds no child of that
    name, sets the value to a default-constructed `RGWObjTagging_S3` and
    returns false (`rgw_xml.h:219-230`), and the caller ignores the return.
    expat runs without namespace processing (`rgw_xml.cc:158`), so a
    prefixed `s3:Tagging` is such a root too.
  - `rebuild` then adds no tag, and `execute` writes the encoded empty set
    over the attr: the object's through `modify_obj_attrs` (`rgw_op.cc:1105`
    at v19.2.6, `:1323` at v20.2.4), the bucket's through
    `merge_and_store_attrs` (`:1199` at v19.2.6, `:1436` at v20.2.4). The
    request answers 200, and every tag is gone.
  - POST object's `tagging` form field takes the same lookup
    (`rgw_rest_s3.cc:3091` at v19.2.6, `:3235` at v20.2.4). There a wrong
    root stores an empty tag set, so the new object is left untagged
    instead of the upload failing.
  - The lookups around it refuse a missing element. Under the root,
    `TagSet` is mandatory (`rgw_tag_s3.cc:56`), so a `Tagging` without one
    answers MalformedXML. PutBucketVersioning checks the return of its root
    lookup and answers 400 (`rgw_rest_s3.cc:2229-2232` at v19.2.6, `:2332`
    at v20.2.4), and PutBucketWebsite passes `mandatory` as true (`:2298`
    at v19.2.6, `:2401` at v20.2.4).
  - The optional lookup came with dc808953f2f (authored 2018-11-01,
    committed 2019-01-03, "rgw: rework lifecycle parsing"). Before it, the
    handlers dereferenced the result of `parser.find_first("Tagging")`
    unchecked.
- **Impact:** a client that sends a Tagging document under another root
  name, or under a prefix, clears the object's or bucket's tags and is told
  it succeeded. AWS is expected to refuse such a body with MalformedXML; that
  is not verified here.
- **Releases:** every release since v14.1.0; checked at v19.2.6, v20.2.4
  and main (7ed73efc1be, 2026-09-25, `rgw_rest_s3.cc:908`, `:999` and
  `:3387`).
- **rgw-go:** mirrors it, so that either gateway answers a request on a
  shared zone alike: `tags.ParseXML` reads a root other than `Tagging` as
  the empty set, and the spec "reads a document whose root is not Tagging as
  the empty set" (`internal/tags/xml_test.go`) pins it. Because rgw-go
  behaves as radosgw does, `docs/exclusions.md` has no entry for it.
- **Upstream:** none for this defect. A prior-art search on 2026-10-01 found
  no issue or fix PR for the silent clearing; it is unfiled while filing is
  paused. Related, and to be cited when it is filed:
  [#44967](https://tracker.ceph.com/issues/44967) (2020) reported the same
  wrong-root input crashing the pre-rework handler, which dereferenced
  `find_first("Tagging")` unchecked.
  [ceph/ceph#34715](https://github.com/ceph/ceph/pull/34715) fixed it on
  mimic, and the luminous backport
  [ceph/ceph#34441](https://github.com/ceph/ceph/pull/34441) closed
  unmerged. Nautilus and later never had that crash, because dc808953f2f had
  replaced the handler; that rework is what brought in this erase, which
  #44967 did not describe.
- **Found:** phase 1 authorization work (unit Z, Task 8), 2026-10-01,
  transcribing the Tagging parse; derived from the source, not reproduced.

## compressor_zlib_winsize 8 writes zlib blocks radosgw cannot read back

- **Kind:** defect, unfixed through main. Reproduced at the zlib library
  level, not on a cluster.
- **Evidence:** paths are under `src/`; each triple of lines is v19.2.6's,
  v20.2.4's and main's (06adccc25d6, 2026-10-01).
  - `compressor_zlib_winsize` is a `type: int` option with `min: -15` and
    `max: 32` (`common/options/global.yaml.in:775-782`, `:775-782`,
    `:828-835`), so 8 is accepted.
  - `ZlibCompressor::zlib_compress` deflates with the option's value and
    stores that same value as the block's `compressor_message`
    (`compressor/zlib/ZlibCompressor.cc:94-100`, `:106-112`, `:109-115`).
  - zlib's `deflateInit2` takes 8 for the zlib wrapper but deflates with a
    9-bit window, and says 9 in the stream's header. zlib.h's
    `deflateInit2` documentation states it: "providing 8 to inflateInit2()
    will result in an error when the zlib header with 9 is checked against
    the initialization of inflate()".
  - `ZlibCompressor::decompress` calls `inflateInit2` with the stored 8
    (`:240`, `:260`, `:263`). inflate refuses the header as "invalid window
    size" (Z_DATA_ERROR), and decompress returns -1 (`:261-266`,
    `:281-286`, `:284-289`). `RGWGetObj_Decompress::handle_data` returns
    that error, so the read fails (`rgw/rgw_compression.cc:148-152` at
    v19.2.6 and v20.2.4).
  - The zlib library check, run with Python's `zlib` module on zlib
    1.3.1.zlib-ng (zlib-ng 2.3.3), was as follows. `compressobj(5,
    DEFLATED, 8)` wrote a stream whose header byte is 0x18, a 9-bit window.
    `decompressobj(8)` and `decompressobj(40)` refused it with "Error -3
    while decompressing data: invalid window size". `decompressobj(9)`,
    `(15)` and `(0)` decoded it. The same run showed which values
    `deflateInit2` takes from the option's range: -15 to -9, 8 to 15 and 25
    to 31. Only 8 is widened. A value it refuses fails the compression, and
    radosgw then stores the object uncompressed
    (`RGWPutObj_Compress::process`, `rgw/rgw_compression.cc:44-88` at both).
  - BlueStore also stores the message with each compressed blob and
    decompresses with it (`os/bluestore/BlueStore.cc:16872-16885` and
    `:12908` at v19.2.6, `:17106-17119` and `:13152` at v20.2.4). That path
    was not traced further.
- **Impact:** with the option at 8 and zlib compressing on its default,
  non-accelerated path, radosgw writes zlib-compressed objects without
  complaint, and every read of them fails. Each accelerator takes over the
  compression when it is enabled and available. All three are off by
  default (`common/options/global.yaml.in:761-766` and `:790-795` at both
  tags, `:806-811` at v20.2.4), and none of them stores the option:
  - ISA-L (`compressor_zlib_isal`, in x86_64 builds with NASM AVX2 and in
    aarch64 builds) stores -15 (`compressor/zlib/ZlibCompressor.cc:155` at
    v19.2.6, `:167` at v20.2.4).
  - QAT (`qat_compressor_enabled`) stores 31 (`compressor/QatAccel.cc:147`
    with `:158` at v19.2.6, `:159` at v20.2.4).
  - UADK (`uadk_compressor_enabled`, at v20.2.4) stores no message
    (`ZlibCompressor.cc:216-219`).
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01); older
  releases not checked.
- **rgw-go:** refuses the block as radosgw does. `inflateMode` takes 8 as
  the zlib wrapper with an 8-bit window, and `openMember` refuses a header
  window wider than that (`internal/compression/zlib.go`). The spec "zlib,
  the header radosgw writes for message 8"
  (`internal/compression/codec_test.go`) pins it.
- **Upstream:** none for this defect. A prior-art search on 2026-10-01 found
  no issue or fix PR; it is unfiled while filing is paused.
- **Found:** phase 1 unit R, Task 2, 2026-10-01, transcribing radosgw's
  zlib decompressor. Reproduced at the zlib library level, not through
  radosgw.

## radosgw's zstd decompress reports success on a frame that fails or decodes short

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/`; `compressor/zstd/ZstdCompressor.h`
  is the same at v19.2.6 and v20.2.4, and main (06adccc25d6, 2026-10-01)
  changed its compressor and includes but not `decompress`. Each pair of
  lines below is the tags', then main's.
  - `ZstdCompressor::decompress` (`:69-102`, `:88-121`) reads the block's
    little-endian length header and allocates that many bytes (`:80`,
    `:99`).
  - It calls `ZSTD_decompressStream` once per input segment without reading
    its result (`:95`, `:114`), and counts the whole segment as consumed
    (`:96`, `:115`).
  - It returns 0 with however many bytes the stream wrote (`:100-101`,
    `:119-120`). So a frame that fails to decode, or that decodes to fewer
    bytes than the header says, yields its partial output as success. A
    frame that decodes to more is cut at the header's length.
  - `RGWGetObj_Decompress::handle_data` delivers whatever decompress
    appended (`rgw/rgw_compression.cc:107-181` at v19.2.6 and v20.2.4).
    The response therefore comes up short, or later blocks' bytes move up
    into the gap.
- **Impact:** a damaged zstd block is served as short or wrong data instead
  of failing the read. radosgw's own compressor writes the header and frame
  consistently, so only a block damaged after it was written meets this.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01); older
  releases not checked.
- **rgw-go:** refuses such a block with `ErrCorrupt`. The zstd decoder
  bounds the frame by the header's length and refuses any other length
  (`internal/compression/zstd.go`). `docs/exclusions.md` records the
  difference.
- **Upstream:** [#77334](https://tracker.ceph.com/issues/77334)
  (2026-06-11), with an open fix,
  [ceph/ceph#69733](https://github.com/ceph/ceph/pull/69733), which checks
  `ZSTD_decompressStream`'s result and requires the decoded length to match
  the block's length header. Not ours to file.
- **Found:** phase 1 unit R, Task 2, 2026-10-01, transcribing radosgw's
  zstd decompressor; derived from the source, not reproduced.

## radosgw's zlib decompress reports success on a truncated stream

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/`; each triple of lines is v19.2.6's,
  v20.2.4's and main's (06adccc25d6, 2026-10-01).
  - `ZlibCompressor::decompress` (`compressor/zlib/ZlibCompressor.cc:214-278`,
    `:230-298`, `:233-301`) inflates until its input runs out
    (`while(remaining)`, `:249`, `:269`, `:272`).
  - It takes `Z_BUF_ERROR` as no error (`:261`, `:281`, `:284`).
  - It returns 0 (`:277`, `:297`, `:300`) whether or not the stream reached
    `Z_STREAM_END`. So a raw deflate, zlib or gzip stream cut short yields
    what it decoded as success, and so does a stream whose trailer is
    missing.
  - zlib's inflate stops without an error when a stream runs short. With
    Python's `zlib` module on zlib 1.3.1.zlib-ng, half of a raw deflate
    stream gave 4152 bytes, no error, and `eof` false. radosgw itself was
    not run.
  - `RGWGetObj_Decompress::handle_data` delivers whatever decompress
    appended (`rgw/rgw_compression.cc:107-181` at v19.2.6 and v20.2.4).
    The response therefore comes up short, or later blocks' bytes move up
    into the gap.
- **Impact:** a truncated zlib block is served as short or wrong data
  instead of failing the read. radosgw's own compressor always finishes the
  stream (`Z_FINISH`), so only a block damaged after it was written meets
  this.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01); older
  releases not checked.
- **rgw-go:** refuses such a block with `ErrCorrupt`. Go's inflaters report
  the truncation (`internal/compression/zlib.go`). `docs/exclusions.md`
  records the difference.
- **Upstream:** none for this defect. A prior-art search on 2026-10-01 found
  no issue or fix PR; it is unfiled while filing is paused.
- **Found:** phase 1 unit R, Task 2, 2026-10-01, transcribing radosgw's
  zlib decompressor; derived from the source, not reproduced.

## radosgw lets a user take an account's email and deletes it with the account

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** `src/rgw/driver/rados/account.cc` is the same at v19.2.6 and v20.2.4 except one
  line after these, so each of its lines below holds at both; main
  (06adccc, 2026-10-01) has the same code 17 lines lower.
  - An account's email index object is the user's: `get_email_obj` names it
    by the lowercased email in `user_email_pool`, under the comment "account
    email oids conflict with user email oids. this ensures that all emails
    are globally unique" (`account.cc:103-114`, main `:120-131`).
  - The account side keeps that promise: `account::write` refuses a new
    email whose object exists, whoever it names, with `-EEXIST`
    (`account.cc:312-322`, main `:329-339`).
  - The user side does not. User create and modify look for a duplicate
    with `get_user_by_email`: `RGWUser::init` under `rgw_user_unique_email`,
    which defaults to true (`driver/rados/rgw_user.cc:1421-1425` at v19.2.6,
    `:1426-1430` at v20.2.4, main `:1366-1370`; `rgw.yaml.in:2441-2448` at
    v19.2.6), and `execute_modify` (`driver/rados/rgw_user.cc:2052-2060` at
    v19.2.6, `:2058-2066` at v20.2.4, main `:2031-2039`). That lookup goes
    through `get_user_info_from_index`, which answers ENOENT when the index
    names an account (`svc_user_rados.cc:700-703` at v19.2.6, `:675-678` at
    v20.2.4, main `:691-694`), so neither check sees the account's email.
  - The user is stored with `exclusive=false` (`driver/rados/rgw_user.cc:1500`
    at v19.2.6, `:1505` at v20.2.4, main `:1445`), and `PutOperation::complete`
    writes the email object with that flag (`svc_user_rados.cc:315-325` at
    v19.2.6, `:295-305` at v20.2.4, main `:302-312`), so the object now names
    the user. The account keeps the email in its info, but `read_by_email`
    answers ENOENT for it (`account.cc:233-236`, main `:250-253`), and an ACL
    grant by that email resolves to the user through `load_owner_by_email`
    (`rgw_sal_rados.cc:1296-1309` at v19.2.6, `:1835-1848` at v20.2.4, main
    `:2067-2080`; `rgw_acl_s3.cc:504-507` at all three).
  - Removing the account then deletes the email object without reading it
    (`account::remove`, `account.cc:418-426`, main `:435-443`), although the
    user now holds it. `account::write`'s rename path, by contrast, deletes
    an old entry only when it names the account (`account.cc:277` and
    `:290`, main `:294` and `:307`).
  - Metadata put repoints another user's email the same way, since it stores
    the user with `exclusive=false` and runs no email duplicate check at all
    (`driver/rados/rgw_user.cc:2831-2833` at v19.2.6, `:2844-2845` at
    v20.2.4), and `PUT /admin/metadata/user` with the `metadata=write` cap
    (`rgw_rest_metadata.cc:263`, `rgw_rest_metadata.h:61-62`),
    `radosgw-admin metadata put` (`rgw_admin.cc:9259` at v19.2.6,
    `radosgw-admin/radosgw-admin.cc:9777` at v20.2.4) and multisite metadata
    sync (`driver/rados/rgw_sync.cc:1118`) all reach it.
- **Impact:** an admin who gives a user an account's email silently takes
  it from the account: the account is no longer found by its email, and a
  grant by that email given after the takeover reaches the user. Once the
  account is removed, the user is not found by its own email either, and
  another user can be given the same email, since the duplicate check finds
  nothing. A grant by that email then fails. An AccessControlPolicy body's
  grant answers 400 UnresolvableGrantByEmailAddress (`rgw_acl_s3.cc:504-507`
  at all three; `rgw_common.cc:70` at v19.2.6, `:71` at v20.2.4, main `:65`).
  An `x-amz-grant-*: emailAddress=` header's grant returns the raw ENOENT
  (`rgw_acl_s3.cc:356-360` at all three), which radosgw sends as 404
  NoSuchKey (`rgw_common.cc:97` at v19.2.6, `:98` at v20.2.4, main `:93`).
- **Releases:** every release with accounts, from v19.1.0; checked at
  v19.2.6, v20.2.4 and main 06adccc.
- **rgw-go:** mirrors it on purpose, so that both gateways answer alike on a
  shared zone. The memstore keeps one email index for users and accounts; a
  user's write repoints an account's entry, and `RemoveAccount` deletes the
  entry whoever holds it. The spec "lets a user take an account's email,
  which the account's removal then drops, as radosgw does"
  (`internal/memstore/admin_test.go`) pins it. The RADOS driver's account
  and user stores are not written yet and are to follow radosgw too.
  Because rgw-go behaves as radosgw does, `docs/exclusions.md` has no entry
  for it.
- **Upstream:** none for this defect. A prior-art search on 2026-10-01 found
  no issue or fix PR; it is unfiled while filing is paused.
- **Found:** phase 1 admin API work (unit N, Task 1 review), 2026-10-01,
  building the memstore's shared user and account email index; derived from
  the source, not reproduced.

## radosgw's LZ4 decompress trusts its block's pair table

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source. A memory-safety defect in Ceph's compressor plugin, which radosgw
  calls to read an object stored with lz4 compression.
- **Evidence:** `src/compressor/lz4/LZ4Compressor.cc`, `LZ4Compressor::decompress`
  (`:99-149` at v19.2.6, `:99-145` at v20.2.4, main 06adccc25d6 one line
  lower).
  - A stored lz4 block starts with a `count` and `count` pairs of
    (original length, compressed length), decoded from the block's own bytes
    (`:109-117` at both tags). The original lengths are summed into a
    `uint32_t total_origin` (`:112`, `:116`), which wraps.
  - The output buffer is allocated from that sum,
    `ceph::buffer::ptr dstptr(total_origin)` (`:120`).
  - Each pair is then decoded with `LZ4_decompress_safe_continue`, passing
    the pair's original length as the output capacity and its compressed
    length as the input length, and advancing both pointers by them
    (`:135-146` at v19.2.6, `:131-142` at v20.2.4). Nothing checks a pair's
    original length against what is left of `dstptr`, or its compressed
    length against what is left of the input.
  - So a pair table whose original lengths wrap to a small `total_origin`
    writes past the end of the heap buffer, and a compressed length larger
    than the remaining input reads past the end of the input buffer. The
    subtraction from `compressed_len` that follows the pair table (`:118`)
    cannot underflow on radosgw's path: `decompress(src, dst)` passes
    `src.length()` (`:95-96`), and a block shorter than its pair table throws
    while the table is decoded (`:114-115`), before it gets there ("[radosgw
    terminates on a stored lz4 block too short for its pair
    table](#radosgw-terminates-on-a-stored-lz4-block-too-short-for-its-pair-table)",
    below).
- **Reachability:** radosgw's own compressor never writes such a block:
  `LZ4Compressor::compress` (`:36-85` at v19.2.6) records each block's true
  lengths, so their sum is the block's real size. An S3 or Swift client
  cannot supply a stored block's bytes. Multisite does not carry one either:
  a fetched object that is not encrypted has its source compression attr
  dropped and is compressed again locally, and only an encrypted and
  compressed object keeps its transferred form, whose blocks the source
  gateway's own compressor wrote (`RGWRadosPutObj::process_attrs`,
  `driver/rados/rgw_rados.cc:3505-3512` at v19.2.6). Reaching the
  defect needs a privileged writer: direct RADOS access to the data pool, or
  a rogue peer zone.
- **Impact:** a crafted stored block corrupts radosgw's heap, or makes it
  read out of bounds, when the object is read.
- **Releases:** checked at v19.2.6, v20.2.4 and main 06adccc25d6.
- **rgw-go:** not affected. `internal/compression`'s lz4 decoder checks the
  pair table against the block's length before allocating, sums the
  original lengths in 64 bits and refuses a total above `math.MaxInt32`,
  sizes its output from that exact sum so every pair fits it, and checks
  each pair's compressed length against the input that remains.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. A prior-art search on 2026-10-01 found no
  issue or fix PR; it is unfiled while filing is paused. Though a
  memory-safety defect, it is disclosed publicly, as every defect here is.
- **Found:** phase 1 unit R, Task 2, 2026-10-01, transcribing radosgw's lz4
  decompressor; derived from the source, not reproduced.

## radosgw hangs or faults walking a manifest whose rule has a stripe size of 0

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** `rgw_obj_manifest.cc` is the same blob at v19.2.6 and
  v20.2.4, and `driver/rados/rgw_obj_manifest.cc` and
  `driver/rados/rgw_obj_manifest.h` differ between them only outside the
  lines below, so each of their lines holds at both.
  - `operator++` on a rule whose `stripe_max_size` is 0 does not move: it
    adds that size to `stripe_ofs` (`rgw_obj_manifest.cc:55`), so it never
    reaches the part's end (`:64`), and sets `ofs` back to `stripe_ofs`
    (`:91`). On a manifest without rules that is not explicit it returns at
    once (`:34-36`).
  - `RGWRados::iterate_obj`, which serves every GET, reads each stripe from
    its offset up to its size and then steps (`driver/rados/rgw_rados.cc:7497-7519`
    at v19.2.6, `:8352-8374` at v20.2.4). A stripe on such a rule has size 0
    (`seek`, `rgw_obj_manifest.cc:162-168`; `operator++`, `:45` and `:86`),
    so the read takes nothing from it, the step stays put, and a GET whose
    range reaches the rule never ends.
  - `obj_find_part` steps from `obj_begin` until it meets part n or a later
    part (`driver/rados/rgw_obj_manifest.cc:210-217`). Once the walk stands
    on such a rule below part n, it never ends.
  - Once part n is found, `get_part_obj_state` reads the state of the object
    holding part n's first stripe and, when that object has a manifest,
    returns at once with it (`driver/rados/rgw_rados.cc:6810-6813` at
    v19.2.6, `:7646-7649` at v20.2.4). Otherwise it gives the part's own
    manifest the found stripe's size as its stripe size (`:6818` at v19.2.6,
    `:7654` at v20.2.4) and fills it in a do-while that steps until the part
    id changes (`:6835-6838` at v19.2.6, `:7671-7674` at v20.2.4).
    - When part n's first stripe is on such a rule, that size is 0, and the
      loop's first `generator::create_next` divides by it
      (`driver/rados/rgw_obj_manifest.cc:28`, reached because
      `set_multipart_part_rule` leaves `max_head_size` at 0,
      `driver/rados/rgw_obj_manifest.h:265-270`); a part head that carries a
      manifest of its own takes the early return instead. On x86-64 that
      integer division raises SIGFPE, whose handler re-raises it with the
      default action, which ends the process (`global/signal_handler.cc:377`,
      `:367` and `:84-102` at v19.2.6 and v20.2.4). AArch64's UDIV yields 0
      for a zero divisor without trapping, so there the do-while would loop
      forever instead (architecture semantics, not exercised).
    - When part n starts in the head, the object holding its first stripe is
      the object itself: `get_obj_state_impl` sets every decoded manifest's
      head to the object read (`set_head`, `driver/rados/rgw_rados.cc:6220` at
      v19.2.6, `:6974` at v20.2.4), and a stripe in the head is located at
      the manifest's object (`rgw_obj_manifest.cc:118-127` and `:200-204`).
      That object's state carries the manifest, so `get_part_obj_state`
      returns at once with the whole object's manifest and state, and the
      do-while never runs. A HEAD then answers 200, because `RGWGetObj`
      returns right after the read's `prepare` for a stat (`rgw_op.cc:2267-2269`
      at v19.2.6, `:2503-2506` at v20.2.4), and a GET goes on to
      `iterate_obj` and never ends there.
  - The part lookup serves GET and HEAD with `partNumber`
    (`RGWRados::Object::Read::prepare`, `driver/rados/rgw_rados.cc:6890` at
    v19.2.6, `:7738` at v20.2.4) and, at v20.2.4, GetObjectAttributes's
    ObjectParts, whose `RadosObject::list_parts` calls both functions
    (`driver/rados/rgw_sal_rados.cc:2866` and `:2898`, from
    `rgw_rest_s3.cc:4082`). `list_parts` also walks the manifest itself,
    stepping past every stripe of a part it has listed (`:2880-2886`), so
    after a part that starts in the head it never ends either. A manifest
    without rules never reaches the part lookup, because its end part id is
    0 and both functions return at once.
  - `update_gc_chain` walks the whole manifest from `obj_begin` to `obj_end`
    (`driver/rados/rgw_rados.cc:5414` at v19.2.6, `:6137` at v20.2.4), and
    `complete_atomic_modification` runs it, unless the tail is kept, on the
    manifest of the object that a write replaces or a delete removes
    (`:5382-5388`, called from `_do_write_meta` at `:3314` and
    `Delete::delete_obj` at `:5963`, at v19.2.6; `:6102-6111`, `:3467` and
    `:6717` at v20.2.4). That walk never ends on such a rule once the
    object passes its head, and on a manifest without rules that is not
    explicit whenever the object is not empty. Each pass whose stripe is not
    the head appends that stripe's object to the chain
    (`cls/rgw/cls_rgw_types.h:1161-1172` at v19.2.6, `:1200-1209` at
    v20.2.4), so the stuck walk also grows memory without bound.
  - radosgw's writers take a rule's stripe size from `rgw_obj_stripe_size`,
    and `generator::create_begin` refuses a manifest without a rule
    (`driver/rados/rgw_obj_manifest.cc:250-254`). With a stripe size of 0,
    `create_next` divides by it at the first offset at or past the head's end
    (`:22-28`), so no such manifest with data past its head is stored ("[radosgw
    divides by zero writing with an rgw_obj_stripe_size of
    0](#radosgw-divides-by-zero-writing-with-an-rgw_obj_stripe_size-of-0)").
    A PUT smaller than the inline head stores one, whose walk ends in the
    head (`rgw_obj_manifest.cc:39-51`) and which is not multipart.
- **Impact:** a manifest that a corrupting or hostile writer with write
  access to the bucket's data pool plants with such a rule past its head:
  - spins a radosgw thread forever on a GET that reads that far, with or
    without `partNumber`;
  - spins one on a GET or HEAD with a `partNumber` past the rule's first
    part, in `obj_find_part`;
  - on x86-64 ends the radosgw process on a GET or HEAD for a part whose
    first stripe is on the rule, unless that part starts in the head, where
    a HEAD answers 200;
  - at v20.2.4 does the same to a GetObjectAttributes request for
    ObjectParts, which also spins in `list_parts`' own walk after a part
    that starts in the head;
  - and spins one on an overwrite or delete of the object, in
    `update_gc_chain`, whose chain grows until memory runs out.
- **Reachability:** a peer zone needs no access to this zone's pools. For an
  encrypted object, multisite fetch decodes the manifest the source zone
  sends and walks it in `read_manifest_parts` with the same `operator++`
  (`RGWRadosPutObj::process_attrs`, `driver/rados/rgw_rados.cc:3536-3543`;
  `rgw_crypt.cc:685-704`, at v19.2.6), so a rogue or compromised peer zone
  can spin the destination's sync worker with a stripe-0 rule. radosgw's
  decrypt setup walks a stored manifest the same way (`rgw_rest_s3.cc:693`
  and `:2833` at v19.2.6).
- **Releases:** every release since v19.0.0 for the part lookup, where
  `obj_find_part` and its caller arrived (8ae61ca5064 and 01d8b4c38bd,
  2023-11-21), and v19.2.6 and v20.2.4 for the other walks, the releases
  checked; also checked at main (7ed73efc1be, 2026-09-25, where
  `create_next`'s division, `obj_find_part`'s loop, `get_part_obj_state`'s
  early return and do-while are unchanged).
- **rgw-go:** refuses the walk. `meta.Manifest.PartBounds` and
  `meta.Manifest.Stripes` fail with `denc.ErrMalformed` at the first step
  that does not move past the previous offset, so for a part that starts in
  the head rgw-go refuses a HEAD that radosgw answers. A listing's sweep of
  a reconciled head's multipart parts (`sweepParts`,
  `internal/driver/list.go`) stops there with a warning, where radosgw's
  `check_disk_state` walk never ends. `docs/exclusions.md` records the
  difference.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. A prior-art search on 2026-10-01 found no
  issue or fix PR; it is unfiled while filing is paused. Related, not a
  duplicate: [#66705](https://tracker.ceph.com/issues/66705) (2024) reported
  an infinite loop in the same part lookup for an upload of a single part,
  which [ceph/ceph#58288](https://github.com/ceph/ceph/pull/58288) fixed;
  that fix does not reach a stripe-0 rule, whose iterator never gets to the
  end. Though a crash and a cross-zone hang, it is disclosed publicly, as
  every defect here is.
- **Found:** phase 1 object read work (unit R, Task 1), 2026-10-01, in
  review of rgw-go's part lookup; derived from the source, not reproduced.

## radosgw divides by zero writing with an rgw_obj_stripe_size of 0

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** `rgw_putobj.cc` and the `driver/rados/rgw_obj_manifest.*`
  lines below are the same at v19.2.6 and v20.2.4.
  - `rgw_obj_stripe_size` is a size option with a default of 4 MiB and no
    minimum (`common/options/rgw.yaml.in:1860-1872` at v19.2.6,
    `:1948-1960` at v20.2.4). radosgw reads it unvalidated.
  - The atomic and multipart writers round it to the data pool's required
    alignment (`get_max_aligned_size`, `driver/rados/rgw_rados.cc:695-708`
    at v19.2.6, `:730-743` at v20.2.4, called at
    `driver/rados/rgw_putobj_processor.cc:317` and `:453` at v19.2.6, `:345`
    and `:487` at v20.2.4), which leaves it unchanged when the alignment is
    0. It is 0 unless the pool is erasure-coded without overwrites
    (`get_required_alignment`, `driver/rados/rgw_rados.cc:659` at v19.2.6,
    `:694` at v20.2.4; `pg_pool_t::requires_aligned_append`,
    `osd/osd_types.h:1754-1756` at v19.2.6, `:1791-1793` at v20.2.4). The
    append writer takes the option raw (`:685` at v19.2.6, `:724` at
    v20.2.4), whatever the pool.
  - With a stripe size of 0, `generator::create_next` divides by it at every
    offset at or past `max_head_size` (`driver/rados/rgw_obj_manifest.cc:22-28`):
    - An atomic PUT sets the rule with `set_trivial_rule(head_max_size, 0)`
      (`driver/rados/rgw_obj_manifest.h:259-263`). The first byte past the
      inline head makes `StripeProcessor::process` ask for the next stripe
      (`rgw_putobj.cc:61-81`), through `ManifestObjectProcessor::next` to
      `create_next` (`driver/rados/rgw_putobj_processor.cc:233-236` at
      v19.2.6, `:261-264` at v20.2.4). A PUT of exactly the head's size
      divides in `complete`'s `create_next` (`:362` at v19.2.6, `:392` at
      v20.2.4). A smaller PUT succeeds and stores a manifest with a
      stripe-0 rule.
    - When the placement keeps no data in the head, or its tail pool
      differs from the head's, `head_max_size` is 0 (`:285-312` at v19.2.6,
      `:313-340` at v20.2.4), and every PUT divides, an empty one included.
    - Every UploadPart divides: `set_multipart_part_rule` leaves
      `max_head_size` at 0 (`driver/rados/rgw_obj_manifest.h:265-270`), so
      the part's first stripe or `complete`'s `create_next` (`:510` at
      v19.2.6, `:546` at v20.2.4) reaches the division.
    - Every append divides the same way (`complete`, `:725` at v19.2.6,
      `:769` at v20.2.4), on any pool.
  - On x86-64 the division raises SIGFPE, whose handler re-raises it with
    the default action, which ends the process (`global/signal_handler.cc:377`,
    `:367` and `:84-102` at v19.2.6 and v20.2.4). This entry does not cover
    other architectures.
- **Impact:** with `rgw_obj_stripe_size` set to 0, radosgw on x86-64 ends
  at the first append on any zone, since append takes the option raw.
  Where the data pool needs no alignment, it also ends at the first PUT
  that reaches the inline head's end, at the first PUT of any size where
  the placement keeps no data in the head, and at the first UploadPart. On
  an erasure-coded pool without overwrites, PUT and UploadPart round the
  stripe size up to the pool's alignment and are unaffected. A PUT smaller
  than the head succeeds and leaves a stripe-0 rule in its manifest, which
  is harmless to read because its walk ends in the head.
- **Releases:** checked at v19.2.6, v20.2.4 and main (7ed73efc1be,
  2026-09-25, where the option still has no minimum and `create_next`'s
  division is unchanged).
- **rgw-go:** does not read `rgw_obj_stripe_size`; it writes no object
  manifests yet. Its manifest walks refuse a stripe-0 rule past the head ("radosgw
  hangs or faults walking a manifest whose rule has a stripe size of 0",
  above).
- **Upstream:** none for this defect. A prior-art search on 2026-10-01 found
  no issue or fix PR; it is unfiled while filing is paused.
- **Found:** phase 1 object read work (unit R, Task 1), 2026-10-01, in
  review of rgw-go's manifest walks; derived from the source, not
  reproduced.

## cls_rgw's omap gc log loses an entry due in the same nanosecond as another

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/`. The omap gc code in
  `cls/rgw/cls_rgw.cc` is the same at v19.2.6, v20.2.4 and main
  (06adccc25d6, 2026-10-01); each triple of lines below is v19.2.6's,
  v20.2.4's and main's.
  - A gc shard holds each omap-era entry twice: under `0_` plus its tag, the
    name index, and under `1_` plus its due time, the time index
    (`gc_index_prefixes`, `:3841`, `:4243`, `:4567`).
  - The time key is the due time alone, formatted `%011llu.%09u` from its
    seconds and nanoseconds; nothing of the tag goes into it
    (`get_time_key`, `:119-125`, `:135-141`, `:124-130`).
  - `gc_update_entry`, which serves both `gc_set_entry` and
    `gc_defer_entry`, sets the due time to `real_clock::now()` plus
    `expiration_secs` (`:3912-3913`, `:4314-4315`, `:4638-4639`). It then
    writes the time key with `cls_cxx_map_set_val` (`:3928`, `:4330`,
    `:4654`), which replaces any value already under the key. So of two
    entries due in the same nanosecond, the later write takes the shared
    time key and the earlier entry's time-index value is gone.
  - `gc_list` reads only the time index (`gc_iterate_entries`,
    `:3996-4084`, `:4398-4486`, `:4722-4810`), so the earlier entry is
    never listed again.
- **What is lost:** the earlier entry's time-index key, not its record.
  - Its `0_` name-index record, chain included, stays in the shard's omap.
    But radosgw's GC processor collects only what `gc_list` returns
    (`RGWGC::process`, `rgw/driver/rados/rgw_gc.cc:550` at v19.2.6, `:565` at
    v20.2.4, main `:487`), so the chain is never collected. Its tail objects
    keep their refcount reference and leak, and the processor never removes
    the record.
  - Removing or deferring the earlier entry's tag later also takes the
    survivor's time key. `gc_remove` and `gc_update_entry` delete the time
    key computed from the stored due time (`:4144`, `:4546`, `:4870`;
    `:3904`, `:4306`, `:4630`), and that key is the shared one, now holding
    the survivor. The survivor then drops out of `gc_list` the same way.
    radosgw reaches this at v19.2.6 and v20.2.4 when it defers the earlier
    tag: `RGWRados::defer_gc` (`rgw_rados.cc:5685` at v19.2.6, `:6373` at
    v20.2.4) calls `RGWGC::async_defer_chain`. That sends `gc_defer_entry`
    through `gc_log_defer1` on an unconverted shard, and `gc_remove` beside
    the queue defer on a converted one (`rgw_gc.cc:180-223` at both tags).
    Main no longer defers.
- **When it happens:** two writes to the same shard whose
  `real_clock::now()` plus `expiration_secs` agree to the nanosecond.
  `real_clock::now()` reads `CLOCK_REALTIME` on the OSD
  (`common/ceph_time.h:115-118`, `:112-115`, `:113-116`).
  - radosgw passes `rgw_gc_obj_min_wait` to every enqueue and defer. With
    one value, the two writes must read the same clock value. That takes a
    realtime clock that steps back, or a shard whose primary moves to an
    OSD whose clock is behind. With different values, as from radosgw
    instances configured differently or a changed setting, two writes whose
    clock readings differ by exactly the difference collide.
  - Only shards still on the omap log are affected. radosgw writes a new
    omap entry only when the rgw_gc queue enqueue fails with ECANCELED or
    EPERM, as on a shard `RGWGC::initialize` has not converted
    (`RGWGC::send_chain`, `rgw_gc.cc:120-139` at both tags, main
    `:121-140`). It defers through the omap log only on a shard it has not
    seen converted (`:180-223` at both tags).
- **Impact:** a rare leak of an object's tail on an unconverted gc shard,
  and of the gc record that names it. Nothing reports it: the shard's
  listing simply never returns the entry.
- **Releases:** v19.2.6, v20.2.4 and main 06adccc25d6; older releases not
  checked.
- **rgw-go:** meets it as radosgw does. `GCSetEntry` and `GCDeferEntry`
  (`internal/cls/rgw/ops_gc.go`) call the same class methods, so the
  overwrite happens in the OSD whichever gateway wrote the entries. Because
  rgw-go behaves as radosgw does, `docs/exclusions.md` has no entry for it.
- **Upstream:** none for this defect. A prior-art search on 2026-10-01 found
  no issue or fix PR; it is unfiled while filing is paused.
- **Found:** phase 1 unit W, Task 2, 2026-10-01, adding cls_rgw's omap-era
  gc methods; derived from the source, not reproduced.

## radosgw's tail stripe names go negative at stripe 2^31 and repeat at 2^32

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/`. The lines below are the same at
  v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01) unless a line is given
  per tag.
  - The manifest generator and its iterator hold the stripe number in an
    `int cur_stripe` (`rgw/driver/rados/rgw_obj_manifest.h:609` and `:509`
    at v19.2.6, `:620` and `:520` at v20.2.4, `:632` and `:532` on main).
  - `generator::create_next` sets it to `(ofs - max_head_size) /
    rule.stripe_max_size`, a u64 quotient converted to int, then adds one in
    part 0 when `max_head_size` is non-zero
    (`rgw/driver/rados/rgw_obj_manifest.cc:28-33`). Ceph builds as C++20
    (`CMakeLists.txt:216` at v19.2.6, `:243` at v20.2.4), which makes the
    conversion modular: only the low 32 bits count, read as signed. Adding one
    to 2^31 - 1 overflows the int, which C++ leaves undefined.
  - `get_implicit_location` names a part-0 tail stripe `"%d", (int)cur_stripe`
    after the prefix (`rgw/rgw_obj_manifest.cc:230`), and a multipart part's
    stripes `".%d_%d"` with the same cast (`:241`), or `".%d"` when the
    stripe is 0 (`:236-237`).
  - So from stripe 2^31 the names go negative, and stripe 2^32 + k takes
    stripe k's name. In part 0 with a head, stripe 2^32 + 1 is named like
    stripe 1; without a head, stripe 2^32 like stripe 0; in a multipart part,
    stripe 2^32 like the part's first stripe.
  - The writer starts each stripe with a `write_full` of its RADOS object:
    `StripeProcessor` hands on offsets relative to the stripe
    (`rgw/rgw_putobj.cc`), and `RadosWriter::process` writes offset 0 with
    `write_full` (`rgw/driver/rados/rgw_putobj_processor.cc:149-150` at
    v19.2.6, `:177-178` at v20.2.4). So the later stripe replaces the earlier
    stripe's data, and the manifest maps both ranges to that one object.
  - A read that walks with `operator++` names stripes as the writer does
    (`rgw/rgw_obj_manifest.cc:56`). A read that seeks past stripe 2^31 also
    misplaces the stripe's start: `seek` computes `stripe_ofs = part_ofs +
    cur_stripe * rule.stripe_max_size` from the negative int (`:151-153`),
    which comes out short by a whole number of 2^32-stripe spans, modulo
    2^64.
- **When it happens:** a part with a stripe numbered 2^31 or more: at
  least 2^31 tail stripes in part 0 behind a head, which numbers them from
  1, or more than 2^31 in a head-less part 0 or a multipart part, which
  number them from 0.
  Neither `rgw_obj_stripe_size` (default 4 MiB) nor `rgw_max_put_size`
  (default 5 GiB) has a `min` or `max` (`common/options/rgw.yaml.in:1860` and
  `:127` at v19.2.6, `:1948` and `:130` at v20.2.4, `:2211` and `:161` on
  main).
  - At the default stripe size behind a 4 MiB head, the names go negative in
    an object over 8 PiB (2^53 bytes) and repeat in one over 16 PiB plus the
    head.
  - A PUT or UploadPart body is capped at `rgw_max_put_size`
    (`RGWPutObj_ObjStore::verify_params` and `get_data`, `rgw/rgw_rest.cc:1053`
    and `:1096` at v19.2.6, `:1058` and `:1101` at v20.2.4), and CopyObject
    checks a non-system request's source against it (`rgw/rgw_op.cc:5628` at
    v19.2.6, `:6194` at v20.2.4). At the default 5 GiB, a PUT behind a 4 MiB
    head, or a multipart part, reaches stripe 2^31 only at a stripe size of 2
    bytes or less, and repeats a name only at 1 byte. At the default stripe
    size, `rgw_max_put_size` must be raised past 8 PiB.
  - radosgw also writes whole objects as part 0 outside a PUT, through
    `copy_obj_data` (`rgw/driver/rados/rgw_rados.cc:5034-5115` at v19.2.6,
    `:5294-5386` at v20.2.4), which checks no size. Lifecycle transition
    (`transition_obj`, `:5117-5184`, `:5417-5490`) and radosgw-admin's object
    and bucket rewrite (`rewrite_obj`, `:3702-3732`, `:3887-3917`, called at
    `rgw/rgw_admin.cc:8101` and `:8312` at v19.2.6,
    `rgw/radosgw-admin/radosgw-admin.cc:8487` and `:8698` at v20.2.4) call it
    without a size check either, and `copy_obj` calls it when a copy must
    move the data (`:4877`, `:5137`), which CopyObject reaches without the
    `rgw_max_put_size` check on a system request (`rgw/rgw_op.cc:5627`,
    `:6193`). Multisite sync writes through `fetch_remote_obj` (`:4264`,
    `:4467`); this entry has not checked what bounds its size.
  - The source of such a copy can be larger than any PUT. A multipart object
    holds up to `rgw_multipart_part_upload_limit` parts (default 10000,
    checked in `RGWCompleteMultipart::execute`, `rgw/rgw_op.cc:6410-6411` at
    v19.2.6, `:7205-7206` at v20.2.4) of up to `rgw_max_put_size` each, about
    48.8 TiB at the defaults. Written as one part-0 object, that reaches
    stripe 2^31 at a stripe size of 24999 bytes or less, and repeats a name
    at 12499 bytes or less.
  - An appendable object has no size limit. `AppendObjectProcessor::prepare`
    checks only that an append starts at the object's current size, and
    adds a part per append (`rgw/driver/rados/rgw_putobj_processor.cc:623-685`
    at v19.2.6, `:662-724` at v20.2.4); only each append's body is capped at
    `rgw_max_put_size`. Its own parts stay small, but grown past 8 PiB, about
    1.7 million appends of 5 GiB, and then transitioned, rewritten or copied
    as above, it is written as one part-0 object and reaches stripe 2^31 at
    the default settings.
- **Impact:** past the thresholds above, a write silently overwrites an
  earlier stripe of the same object, and reads of the earlier range no longer
  return its data. A PUT or a multipart part gets there only with a stripe
  size of a few bytes or a `rgw_max_put_size` in petabytes, but at the
  default settings an appendable object grown past 8 PiB gets there when
  lifecycle transitions it, an admin rewrites it, or a system-request
  CopyObject copies its data.
- **Releases:** v19.2.6, v20.2.4 and main 06adccc25d6; older releases not
  checked.
- **rgw-go:** mirrors it on purpose, so both gateways name stripes alike on a
  shared zone. The writer's `TailObj` (`internal/meta/manifest_write.go`) and
  the reader's iterator call one `implicitLocation`
  (`internal/meta/manifest_iter.go`), which names a stripe by its low 32
  bits as an int; the spec "names a stripe by its index as an int, as
  get_implicit_location prints (int)cur_stripe"
  (`internal/meta/manifest_write_test.go`) pins it. The reader's seek
  computes the stripe's start as the C++ does. Go's int32 arithmetic wraps
  where the C++ increment is undefined. Because rgw-go behaves as radosgw
  does, `docs/exclusions.md` has no entry for it.
- **Upstream:** none for this defect. A prior-art search on 2026-10-02 found
  no issue or fix PR; it is unfiled while filing is paused. The stripe
  counters are themselves `int` in both the iterator and the generator, so a
  fix widens them, not only the name's format.
- **Found:** phase 1 unit W, Task 3, 2026-10-01, writing the plain-PUT
  manifest generator; derived from the source, not reproduced.

## radosgw's user removal reports success over a lost version race, leaving the user without its indexes

- **Kind:** defect. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines below is
  v19.2.6's, then v20.2.4's.
  - `RGWSI_User_RADOS::remove_user_info` first removes the user's active
    access-key index objects, its Swift-key index objects, its email index
    object and, for a user outside an account, its bucket list, the
    `<uid>.buckets` object (`services/svc_user_rados.cc:559-600`,
    `:533-574`). It then unlinks the user from its account and its groups
    (`:601-622`, `:575-596`). Only after that does it remove the user's
    `users.uid` object, through `remove_uid_index` under the caller's version
    tracker (`:624-627`, `:598-601`).
  - `remove_uid_index` returns 0 when that removal fails with ENOENT or with
    ECANCELED (`:639`, `:614-616`; v20.2.4 comments the return "success but
    no mdlog entry"), and `remove_user_info` then returns 0.
  - The tracker carries the version the user was loaded at:
    `RadosUser::remove_user` passes the `objv_tracker` that `load_user`
    filled (`driver/rados/rgw_sal_rados.cc:264-267` and `:278-282`,
    `:281-284` and `:295-299`). The removal therefore runs cls_version's
    `check_conds` on the user object, which fails with ECANCELED once
    another writer has changed the user (`cls/version/cls_version.cc:177-197`
    at both tags).
  - `RGWUser::init` loads the user, and with it the version tracker, before
    the removal starts (`driver/rados/rgw_user.cc:1418` and `:1442`, `:1423`
    and `:1447`). `RGWUser::execute_remove` then lists the user's buckets
    through its bucket list and, with purge-data, removes them, and only then
    calls `remove_user` on the user `init` loaded (`:1945-1985`,
    `:1951-1991`). The window between the load and the removal spans the
    bucket purge.
- **Impact:** a `radosgw-admin user rm`, or an admin API user removal, that
  races a change to the same user, such as a key added or the user
  suspended, reports success. The user object survives at its new version.
  - The index objects of the access keys, Swift keys and email of the user as
    `init` loaded it are gone, so the user cannot authenticate with those
    keys and is not found by that email. A key or email that the racing
    change added keeps the index object that change wrote
    (`services/svc_user_rados.cc:315-351`, `:295-331`), so the user can
    still authenticate with such a key.
  - Its bucket list is gone too, so a bucket it created after the removal
    listed its buckets is missing from its ListBuckets. `user info --uid`
    still shows the user.
  - Running the removal again deletes the user but not such a bucket. With
    the bucket list gone, the listing finds no buckets
    (`driver/rados/buckets.cc:108-111` at both tags), so the re-run neither
    refuses without purge-data nor purges with it
    (`driver/rados/rgw_user.cc:1956-1983`, `:1962-1989`). The bucket is left
    owned by a deleted user.
- **Releases:** checked at v19.2.6 and v20.2.4; older releases not checked.
- **rgw-go:** keeps radosgw's order, so a lost race leaves the same state.
  `RemoveUser` (`internal/driver/user.go`) reports the race instead of
  success: ConcurrentModification when the user changed, NoSuchUser when it
  is gone. Whether the admin API's user removal passes either error on to
  the client is decided with that op.
- **Upstream:** none for this defect. A prior-art search on 2026-10-02 found
  no issue or fix PR; it is unfiled while filing is paused.
- **Found:** phase 1 metadata plane (unit M, Task 4), 2026-10-01,
  implementing the RADOS driver's RemoveUser; derived from the source, not
  reproduced.

## radosgw's object stat ignores a data pool it cannot resolve

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `get_obj_state_impl` calls `obj_to_raw` and drops its result
    (`driver/rados/rgw_rados.cc:6144-6145`, `:6898-6899`), so when
    `rgw_get_obj_data_pool` resolves neither the bucket's placement nor the
    zonegroup's default placement (`driver/rados/rgw_obj_manifest.cc:411-430`
    at both) the raw object keeps an empty pool. `rgw_obj_select::get_raw_obj`
    drops the same result for tail objects (`:441-449` at both).
  - `raw_obj_stat` opens that pool through `get_raw_obj_ref`, which replaces
    only an empty oid (`driver/rados/rgw_rados.cc:2530-2543`, `:2638-2650`),
    and `rgw_get_rados_ref`, which opens with create set
    (`driver/rados/rgw_tools.cc:102-117`, `:103-118`).
  - `rgw_init_ioctx` finds no pool named "" and calls `pool_create("")`
    (`:29-31`, `:30-32`), which librados refuses with EINVAL before it
    contacts a monitor (`librados/RadosClient.cc:683-687`, `:685-689`), so
    nothing is created on the cluster. The stat fails with EINVAL, which the
    S3 error table renders as 400 InvalidArgument (`rgw/rgw_common.cc:61`,
    `:62`).
  - The head-object helpers check the same result and answer -EIO,
    "probably misconfiguration" (`get_obj_head_ioctx`,
    `driver/rados/rgw_rados.cc:2483-2487`, `:2590-2595`; `get_obj_head_ref`,
    `:2508-2512`, `:2616-2620`), which the S3 error table lacks, so they
    answer 500 UnknownError where the stat answers 400.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01, where
  `get_obj_state_impl` still drops the result at `rgw_rados.cc:7448`).
- **rgw-go:** the driver's pool resolution answers 500 UnknownError, naming
  the placement, as radosgw's head-object helpers do, for a multipart
  upload's meta object in the data-extra pool as for an object's head;
  `docs/exclusions.md` records the difference from radosgw's stat, which
  `RadosMultipartUpload::get_info` reaches too
  (`driver/rados/rgw_sal_rados.cc:3672` at v19.2.6, `:4531` at v20.2.4).
- **Upstream:** none for this defect. A prior-art search on 2026-10-02 found
  no issue or fix PR; it is unfiled while filing is paused.
- **Found:** phase 1 unit R, Task 3, 2026-10-01, transcribing radosgw's
  object stat for the driver; derived from the source, not reproduced.

## radosgw never finishes a GET on a zero rgw_get_obj_max_req_size

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `rgw_get_obj_max_req_size` is a size option with no minimum
    (`common/options/rgw.yaml.in:1908-1918`, `:2008-2018`).
  - `Read::iterate` passes it to `iterate_obj` as the longest piece one read
    takes (`driver/rados/rgw_rados.cc:7449` and `:7455-7456`, `:8300` and
    `:8310-8311`). `iterate_obj` caps every piece at it (`:7506-7508` and
    `:7523`, `:8361-8363` and `:8378`), so at 0 every piece is empty, and
    both of its loops advance by the piece's length (`:7516-7517` and
    `:7530-7531`, `:8371-8372` and `:8385-8386`), so neither ends while a
    byte of the range remains.
  - Each pass calls `get_obj_iterate_cb`. A piece the prefetched head covers
    returns at once (`:7408-7421`, `:8259-8272`); any other sends a RADOS
    read (`:7425-7441`, `:8276-8292`).
- **Impact:** with the option at 0, a GET of a non-empty object never
  finishes, and it does not merely stall: each pass of the loop sends
  another RADOS read for a piece the prefetched head does not cover, and
  the read throttle never holds one back. A read's cost is its length
  (`rgw_rados.cc:7436`, `:8287`), and both throttles wait only once
  `pending_size` passes the window (`rgw_aio_throttle.cc:45-49` and
  `:136-143`, `rgw_aio_throttle.h:34`, at both tags), so a zero-cost read
  never waits. The worker pegs a CPU and issues RADOS reads without bound;
  whether the objecter's own in-flight limits bound the reads outstanding
  at the OSDs is not checked. HEAD does not iterate.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01, where
  the option still has no minimum, `rgw.yaml.in:2268-2277`, and
  `iterate_obj` caps the piece the same way, `rgw_rados.cc:9022` and
  `:9038`).
- **rgw-go:** refuses to start on a zero `rgw_get_obj_max_req_size`, naming
  the value; `docs/exclusions.md` records the difference.
- **Upstream:** none for this defect. A prior-art search on 2026-10-02 found
  no issue or fix PR; it is unfiled while filing is paused.
- **Found:** phase 1 unit R, Task 3, 2026-10-01, in review of the driver's
  read options; derived from the source, not reproduced.

## rados_nobjects_list_seek reports the position it was given, not the one it lands at

- **Kind:** documentation defect: librados.h promises the rounded
  position, the implementation returns its argument, and upstream's tests
  pin the implementation. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`; each pair of lines below is
  v19.2.6's, then v20.2.4's.
  - librados.h documents `rados_nobjects_list_seek` as returning the "actual
    (rounded) position we moved to" (`include/rados/librados.h:1098`,
    `:1099`), and `rados_nobjects_list_get_pg_hash_position` as returning the
    hash position "rounded to the current pg" (`:1089`, `:1090`).
  - `Objecter::list_nobjects_seek(NListContext*, uint32_t)` sets the position
    to `hobject_t(hash=pos)` and `current_pg` to the hash's placement group,
    and returns `pos` unchanged (`osdc/Objecter.cc:3761-3773`,
    `:3934-3946`). `get_pg_hash_position` returns `pos.get_hash()`, also
    unrounded (`osdc/Objecter.h:2247-2249`, `:2214-2216`).
  - Where the listing then resumes depends on whether it has fetched. A
    fresh listing (`rados_nobjects_list_open`,
    `librados/librados_c.cc:2364-2379` at both tags) has `sort_bitwise`
    false (`osdc/Objecter.h:2223`, `:2190`). The integer seek leaves it so,
    unlike the cursor seek, which sets it (`osdc/Objecter.cc:3784`, `:3957`).
    The first fetch then takes the cluster's SORTBITWISE flag, which every
    active OSD requires (`osd/OSD.cc:8939-8942`, `:9135-9138`), as a change
    of sort order, and restarts at `hobject_t(hash=current_pg)`, the first
    position of the placement group (`osdc/Objecter.cc:3828-3835`,
    `:4001-4008`).
  - Once a listing has fetched, `sort_bitwise` is true, `IoCtxImpl::nlist_seek`
    leaves it so (`librados/IoCtxImpl.cc:538-543`, `:545-550`), and a seek
    lands on the hash itself.
- **Impact:** a caller that seeks a fresh listing to a hash gets that hash
  back, but the listing resumes at the start of its placement group and
  first returns objects that come before the hash in the listing's order. A
  caller that seeks a listing that has fetched resumes at the hash. Neither
  the return value nor `rados_nobjects_list_get_pg_hash_position` tells the
  caller which happened. The rounding to a placement group is what
  librados.h promises.
- **Releases:** checked at v19.2.6 and v20.2.4; older releases not checked.
- **rgw-go:** unaffected by where the seek lands. `radosclient.ListPage`
  (`internal/radosclient/listpage.go`) skips every entry hobject order puts
  at or before the resume point and ignores the seek's return value, so a
  seek that lands at the group's start costs a replay, recorded in
  `docs/cgo-limitations.md`, and repeats nothing.
- **Upstream:** none for this defect. A prior-art search on 2026-10-02 found
  no issue or fix PR; it is unfiled while filing is paused. The return value
  is intended: upstream's own test asserts that the seek returns its
  argument (`src/test/librados/list.cc:181` and `:384` at v19.2.6), so the
  fix is to librados.h's `@returns`, with a note on the fresh-listing
  restart.
- **Found:** phase 1 unit N, Task 2, 2026-10-01, implementing
  `Pool.ListObjectsFrom`; derived from the source, not reproduced.

## librados aborts the process on a listing seek after its pool is deleted

- **Kind:** defect. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`; each pair of lines below is
  v19.2.6's, then v20.2.4's.
  - Both `Objecter::list_nobjects_seek` overloads map the position to a
    placement group with `osdmap->raw_pg_to_pg` (`osdc/Objecter.cc:3769`
    and `:3782`, `:3942` and `:3955`) without checking that the listing's
    pool is still in the client's OSD map, and `OSDMap::raw_pg_to_pg`
    `ceph_assert`s that it is (`osd/OSDMap.h:1432-1436`, `:1464-1468`).
  - Opening a listing checks nothing either: `rados_nobjects_list_open`
    copies the I/O context's pool id, snap seq and namespace into a new
    listing context and asks the cluster nothing
    (`librados/librados_c.cc:2364-2379` at both tags).
  - A listing that does not seek meets the deletion in
    `Objecter::list_nobjects`, which answers ENOENT
    (`osdc/Objecter.cc:3813-3819`, `:3986-3992`).
    `rados_nobjects_list_next2` passes that on, and returns the same ENOENT
    at a listing's end (`librados/librados_c.cc:2473-2481` at both tags).
  - radosgw's `rgw_list_pool` seeks every listing it makes with a cursor. It
    parses its marker, an empty one as `hobject_t()`, and opens the listing
    with `nobjects_begin(cursor)` (`rgw/driver/rados/rgw_tools.cc:354-361`,
    `:370-377`; `librados/librados_cxx.cc:3066-3073`, `:3073-3080`), which
    seeks through `rados_nobjects_list_seek_cursor`
    (`librados/librados_cxx.cc:1897-1908`, `:1904-1915`, and `:822-827` at
    both tags). It serves radosgw's system-object pool listing
    (`rgw/services/svc_sys_obj_core.cc:594` and `:637` at both tags).
    radosgw's other listings open with the argument-less `nobjects_begin()`,
    which does not seek (`librados/librados_cxx.cc:1871-1882`,
    `:1878-1889`). Those are the log pool listing
    (`rgw/driver/rados/rgw_rados.cc:1491`, `:1594`), the cursor-less
    `pool_iterate_begin` (`:9101`, `:10046`) and the orphan search
    (`rgw/rgw_orphan.cc:306` at v19.2.6, `rgw/radosgw-admin/orphan.cc:310`
    at v20.2.4).
- **When it happens:** a seek on an I/O context opened before its pool was
  deleted, once the client has the OSD map without the pool.
- **Impact:** the client process aborts instead of getting an error. For
  radosgw that is any `rgw_list_pool` call on such a context; for rgw-go, a
  resumed `Pool.ListObjectsFrom` page.
- **Releases:** checked at v19.2.6 and v20.2.4; older releases not checked.
- **rgw-go:** affected. goceph's `ListObjectsFrom` seeks before its first
  fetch whenever a token is set, and goceph pools and reuses its I/O
  contexts (`internal/radosclient/goceph/pool.go`), so a context can predate
  the deletion. A pre-check would race the deletion, so goceph has none.
  A first page does not seek, and reads the deletion as an empty listing
  because go-ceph's `Iter.Err` takes ENOENT for the end.
  `docs/cgo-limitations.md` records both.
- **Upstream:** none for this defect. A prior-art search on 2026-10-02 found
  no issue or fix PR; it is unfiled while filing is paused. Related but
  distinct: [#51846](https://tracker.ceph.com/issues/51846), a LibRadosList
  cursor test that did not complete, and
  [#20632](https://tracker.ceph.com/issues/20632), the same assert string on
  a hammer to jewel upgrade, closed Won't Fix.
- **Found:** phase 1 unit N, Task 2 review, 2026-10-01; derived from the
  source, not reproduced.

## radosgw drops all but the first OIDC provider or Service principal in a statement

- **Kind:** defect, security-relevant. The OIDC half is fixed on main after
  v20.2.4 and not backported; the Service half is unfixed through main.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines below is
  v19.2.6's, then v20.2.4's.
  - A statement keeps its Principal and NotPrincipal entries in a
    `boost::container::flat_set<rgw::auth::Principal>`
    (`rgw_iam_policy.h:536-537`, `:589-590`), and the parser adds each with
    `pri.emplace` (`rgw_iam_policy.cc:733-734`, `:745-746`). A flat set's
    emplace keeps the first of two equivalent principals and drops the
    second.
  - `Principal`'s `==` and `<` compare only its kind and its `rgw_user`,
    the tenant and the id (`rgw_basic_types.h:228-234`, `:245-251`). An OIDC
    provider principal holds its URL in `idp_url` and leaves the `rgw_user`
    empty (`:155-156`, `:158-159`); the parser builds it from the
    `oidc-provider/<url>` resource of an AWS or Federated ARN
    (`rgw_iam_policy.cc:554-556`, `:564-566`). v20.2.4's Service principal
    holds its name in `service_id`, again with an empty `rgw_user`
    (`rgw_basic_types.h:187-191`; `rgw_iam_policy.cc:591-592`).
  - So any two OIDC providers in one statement's list are equivalent, and so,
    on v20.2.4, are any two services: the list keeps the first in document
    order. The identities match on what was dropped: a web identity matches
    an OIDC principal by `idp_url` (`WebIdentityApplier::is_identity`,
    `rgw_auth.cc:762-766`, `:778-782`), and v20.2.4's service identity
    matches by `service_id` (`ServiceIdentity::is_identity`,
    `rgw_auth.h:877-879`).
  - A statement whose Principal list does not hold the identity does not
    apply to it, and one whose NotPrincipal list does not hold it applies
    (`rgw_iam_policy.cc:1196-1198`, `:1253-1255` and `:1271-1273`;
    `:1200-1202`, `:1257-1259` and `:1275-1277`).
- **Impact:**
  - A Deny statement whose Principal names two OIDC providers does not
    apply to the second, so that provider's web identity escapes the Deny
    wherever another statement allows it. radosgw evaluates OIDC principals
    in a role's trust policy, at AssumeRoleWithWebIdentity
    (`rgw_rest_sts.cc:860`, `:864`).
  - In a NotPrincipal list the dropped principal is no longer excepted. A
    Deny with NotPrincipal then applies to it. An Allow with NotPrincipal,
    which v19.2.6 accepts and v20.2.4 refuses at parse
    (`rgw_iam_policy.cc:773-776` at v20.2.4), then grants it.
  - Two Service principals collapse the same way on v20.2.4. Its only
    service identity is bucket logging's (`rgw_bucket_logging.cc:989`), so
    that half reaches only the bucket-logging target check.
- **Releases:** checked at v19.2.6 and v20.2.4, and at the squid and
  tentacle branch heads (a742f50616e, 2026-09-03; 7411a080411, 2026-09-30),
  whose `==` compares only the kind and the `rgw_user`; older releases not
  checked. On main (06adccc25d6, 2026-10-01) `==` and `<` also compare
  `idp_url`, so OIDC providers no longer collapse, but not `service_id`
  (`rgw_basic_types.h:247-255`).
- **rgw-go:** reproduces it: `policy.Parse` keeps a statement's principals
  once each by kind, account and id, as the flat set does, so of two OIDC
  providers, or two services on Tentacle, it keeps the first
  (`internal/policy/parse.go`). `policy.Principal` itself keeps the URL and
  the service name, and Go's `==` compares them.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response.
  - The OIDC half is fixed on ceph main by 1a780f21758, "rgw/oidc: enforce
    trust policy principal scope for OIDC providers", which adds `idp_url`
    to `==` and `<`. It came with
    [ceph/ceph#68850](https://github.com/ceph/ceph/pull/68850), merged
    2026-09-03 for tracker [#76069](https://tracker.ceph.com/issues/76069).
    That pull request adds global OIDC providers; it is a feature, not a fix
    for this defect, and is not backported: no v19 or v20 tag contains
    1a780f21758, nor do the squid and tentacle branch heads above.
  - The Service half: none. A prior-art search (2026-10-02) found no report
    or fix; it is found by us and latent, since v20.2.4's only service
    identity is bucket logging's. Severity estimate from triage: an
    authorization bypass a tenant can set through its own bucket, role-trust
    or session policy, with the OIDC half around CVSS 6.5 to 7.1 (triage
    estimate). It is unfiled while filing is paused.
- **Found:** phase 1 unit Z, Task 2, 2026-10-01, transcribing
  `rgw::auth::Principal`; derived from the source, not reproduced.

## radosgw cannot load a bucket whose entry point is from before version 8

- **Kind:** defect, a regression since Octopus. Unreproduced: derived from
  the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - radosgw wrote entry points below version 8 before Dumpling (0.67). Such
    an entry point is itself an `RGWBucketInfo`: `RGWBucketEntryPoint::decode`
    decodes it into `old_bucket_info`, sets `has_bucket_info` and leaves
    `bucket` empty (`rgw_common.h:1134-1142`, `:1177-1185`).
  - Every bucket request without an instance id, which only a system
    request's `rgwx-bucket-instance` parameter supplies (`rgw_op.cc:498-505`,
    `:528-535`), loads its bucket by name (`:528-541`, `:558-571`) through
    `RadosStore::load_bucket`
    (`driver/rados/rgw_sal_rados.cc:1595-1600`, `:2135-2140`) and
    `RadosBucket::load_bucket` (`:610-637`, `:631-655`), and
    `RGWRados::get_bucket_info` and `try_refresh_bucket_info` load the same way
    (`driver/rados/rgw_rados.cc:8995-9026`, `:9940-9971`). Each reads through
    `RGWBucketCtl::read_bucket_info`, which reads the entry point and then
    the instance its `bucket` names, without looking at `has_bucket_info`
    (`driver/rados/rgw_bucket.cc:3085-3129`, `:3234-3273`).
  - For such an entry point `bucket` is empty, and so is its key
    (`services/svc_bucket.cc:21-24` and `rgw_basic_types.cc:62-79` at both
    tags), so the instance read is of `.bucket.meta.` in the domain root, an
    object no radosgw writes. The load fails with ENOENT, which
    `rgw_build_bucket_policies` answers as NoSuchBucket (`rgw_op.cc:531-540`,
    `:561-570`).
  - `RGWSI_Bucket_SObj::read_bucket_info` still serves the embedded info
    (`services/svc_bucket_sobj.cc:434-439`, `:377-382`), but only the bucket
    stats of an owner's bucket listing and the multisite sync hints call it
    (`:620`, `:558`; `services/svc_bucket_sync_sobj.cc:85`, `:84`). The
    conversion, `convert_old_bucket_info`, runs only inside
    `set_bucket_instance_attrs` (`driver/rados/rgw_bucket.cc:3237-3304`,
    `:3359-3421`), which writes a bucket already loaded.
  - Nautilus's `RGWRados::_get_bucket_info` served the embedded info
    (`rgw_rados.cc:8482-8489` at v14.2.22). Octopus's first releases already
    load through an `RGWBucketCtl::read_bucket_info` without the branch
    (`rgw_bucket.cc:3198` at v15.1.0, `:3211` at v15.2.0, `:3279-3319` at
    v15.2.17), where `has_bucket_info` is read only by the svc function and
    `convert_old_bucket_info`.
- **Impact:** a bucket whose entry point is still from before version 8, on
  a cluster that has run radosgw since before Dumpling, answers
  NoSuchBucket to every S3 request once radosgw is Octopus or later. Its
  data stays in place, and no S3 request reaches the conversion. Rook's
  clusters, which postdate Dumpling, hold none.
- **Releases:** v15.1.0, v15.2.0, v15.2.17, v19.2.6 and v20.2.4; v14.2.22
  serves the bucket.
- **rgw-go:** `GetBucket` (`internal/driver/bucket.go`) loads a bucket as
  `load_bucket` does and answers NoSuchBucket the same way. `PutBucketAttrs`
  reads the entry point where `set_bucket_instance_attrs` does, and refuses
  a bucket that needs the conversion (`docs/exclusions.md`, "An entry point
  from before version 8 is not converted").
- **Upstream:** none. A prior-art search on 2026-10-02 found no issue or fix
  PR; it is unfiled while filing is paused. The regression came with
  a46cf9ea2d7 ("rgw: ctl.bucket: add read_bucket_info()", 2019), which is in
  v15.1.0 and not in v14.2.22.
- **Found:** phase 1 metadata plane (unit M, Task 5), 2026-10-01,
  implementing the RADOS driver's GetBucket; derived from the source, not
  reproduced.

## radosgw takes any prefix of bytes for a Range's unit

- **Kind:** defect, unfixed through main. Unreproduced on a cluster: derived
  from the source and checked by running the function.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `RGWGetObj::parse_range` takes a Range value without "bytes=" in it by
    skipping whitespace to the unit, then comparing
    `strncasecmp(rs.c_str(), "bytes", end - pos)`: the unit's length of
    bytes, but from the value's first byte rather than the unit's
    (`rgw_op.cc:170-184`, `:213-227`). A unit that the compare passes is
    read as bytes up to the "=".
  - So a unit that is a prefix of "bytes" in any case, such as "b" or
    "byt", and an empty unit, as in "=0-1", are taken for bytes, and the
    range is served as 206. A unit after leading whitespace is compared
    against the whitespace and the value is served whole, though HTTP
    parsers strip leading whitespace from a field value, so that case
    rarely arrives.
  - parse_range, copied from v19.2.6 and run on glibc 2.42, gives 0-1 with
    partial content for "b=0-1", "byt=0-1" and "=0-1", and the whole object
    without partial content for "  bytes = 0-1".
  - RFC 9110, section 14.2, says a server must ignore a Range header field
    whose range unit it does not understand.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01, where
  the compare is unchanged at `rgw_op.cc:260`).
- **rgw-go:** `op.ParseRange` mirrors it, so a GET answers as radosgw's
  does.
- **Upstream:** an open fix PR that predates this entry,
  [ceph/ceph#71300](https://github.com/ceph/ceph/pull/71300) ("rgw: parse
  the Range header field per RFC 9110", opened 2026-08-24, in review),
  replaces this unit comparison. A prior-art search on 2026-10-02 found no
  tracker issue. The comparison dates from b8a3baffddb (2018). Related:
  [#13412](https://tracker.ceph.com/issues/13412), multi-range GET support.
  Not filed: a report would duplicate the PR.
- **Found:** phase 1 unit R, Task 5's preflight, 2026-10-01, transcribing
  parse_range for the GetObject op; derived from the source, not reproduced
  on a cluster.

## radosgw's parse_time drops a numeric zone offset

- **Kind:** defect, unfixed through main. Unreproduced on a cluster: derived
  from the source and checked by running the function.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `parse_time`, which reads If-Modified-Since and If-Unmodified-Since,
    tries RFC 1123 with a numeric zone among its formats
    (`parse_rfc1123_alt`, `"%a, %d %b %Y %H:%M:%S %z"`,
    `rgw_common.cc:587-592`, `:600-605`). glibc's strptime parses the zone
    into `tm_gmtoff`.
  - `parse_time` then converts the fields with `internal_timegm`
    (`rgw_common.cc:711`, `:724`), which reads the year, month, day, hour,
    minute and second alone (`include/timegm.h:54-77` at both), so the
    offset is dropped: "Sat, 29 Oct 2100 19:43:31 +0100" reads as 19:43:31
    UTC, an hour after the instant it names.
  - radosgw's SigV2 date check, which parses with the same formats, does
    apply the offset: `*header_time -= t.tm_gmtoff` (`rgw_auth_s3.cc:242`,
    `:244`).
  - parse_time, copied from v19.2.6 and run on glibc 2.42, gives
    2100-10-29T19:43:31Z for "+0100", "-0800" and "+01:30" alike.
- **Impact:** If-Modified-Since and If-Unmodified-Since on GET and HEAD
  (`rgw_op.cc:2475` and `:2481`, `:2707` and `:2713`) and CopyObject's
  x-amz-copy-source-if-modified-since and -if-unmodified-since (`:5504` and
  `:5512`, `:6070` and `:6078`) compare against an instant off by the
  offset. RFC 9110's HTTP-date is always GMT, so only a client that sends a
  numeric zone meets it.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01,
  `rgw_common.cc:605` and `:725`).
- **rgw-go:** `op.ParseHTTPTime` mirrors it, so a conditional answers as
  radosgw's does.
- **Upstream:** two fix PRs for this drop of the parsed offset, both closed
  unmerged: [ceph/ceph#20453](https://github.com/ceph/ceph/pull/20453)
  (2018, "Use gmtoff when converting struct tm to internal time format") and
  [ceph/ceph#34083](https://github.com/ceph/ceph/pull/34083) (2020,
  "common/utime : create tv to get gmtoff"). A prior-art search on
  2026-10-02 found no tracker issue. It is unfiled while filing is paused; a
  report would cite both PRs.
- **Found:** phase 1 unit R, Task 5's preflight, 2026-10-01, transcribing
  parse_time for the GetObject op; derived from the source, not reproduced
  on a cluster.

## radosgw's parse_time wraps a date outside 1970 to 2106

- **Kind:** defect, unfixed through main. Unreproduced on a cluster: derived
  from the source and checked by running the function.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `parse_time` returns `utime_t(sec, ns).to_real_time()`
    (`rgw_common.cc:712`, `:725`), and utime_t keeps its seconds in a
    `__u32` (`include/utime.h:51` and `:72` at both), so the time_t
    `internal_timegm` computed is taken modulo 2^32. A date before
    1970-01-01 or after 2106-02-07T06:28:15Z wraps.
  - parse_time, copied from v19.2.6 and run on glibc 2.42, reads
    "Thu, 01 Jan 1960 00:00:00 GMT" as 2096-02-06T06:28:16Z and
    "Sun, 07 Feb 2106 06:28:16 GMT" as 1970-01-01T00:00:00Z. asctime's and
    ISO 8601's years are read as written, up to four digits, so
    "Sat Oct 29 19:43:31 94" is the year 94, which reads as
    2000-04-04T14:19:15Z.
  - radosgw's SigV2 date check refuses a year before 1970 for the same
    parse (`rgw_auth_s3.cc:237-240`, `:239-242`); parse_time does not.
- **Impact:** an If-Modified-Since before 1970 reads as a date in the
  2090s, so radosgw answers 304 Not Modified for every object written
  before then, where the client asked for any object modified since; an
  If-Unmodified-Since before 1970 lets the read through where it should
  fail with 412. CopyObject's source conditionals go the same way.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01,
  `rgw_common.cc:726`, and `include/utime.h:71`).
- **rgw-go:** `op.ParseHTTPTime` mirrors it, so a conditional answers as
  radosgw's does.
- **Upstream:** none for this site. A prior-art search on 2026-10-02 found
  no issue or fix PR; it is unfiled while filing is paused. The wrap is
  `utime_t`'s 32-bit seconds, a known limit of Ceph's wire time:
  [ceph/ceph#20965](https://github.com/ceph/ceph/pull/20965), closed
  unmerged, proposed widening them, and
  [ceph/ceph#21113](https://github.com/ceph/ceph/pull/21113) is a related
  merged fix in `utime_t`. A report would ask `parse_time` to refuse a date
  it cannot represent rather than wrap it.
- **Found:** phase 1 unit R, Task 5, 2026-10-02, testing parse_time's
  arithmetic against a copy of it; derived from the source, not reproduced
  on a cluster.

## radosgw sends no response, or two status lines, when it refuses a response-* parameter

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's, and a single line is the same at both.
  - `RGWGetObj_ObjStore_S3::send_response_data` refuses a `response-*`
    parameter on an anonymous request, or one whose value holds a control
    character, with -ERR_INVALID_REQUEST (`rgw_rest_s3.cc:517-533`,
    `:634-650`). It does so while it writes the response headers: after
    `dump_errno` has sent the success status and `dump_content_length` the
    length (`:400` and `:459`, `:411` and `:495`), and before `end_header`
    and `sent_header = true` (`:617-649`, `:734-766`). The success status
    is 200, or 206 when parse_range found a range (`partial_content`,
    `rgw_rest_s3.cc:398`, `:409`, set at `rgw_op.cc:192`, `:235`).
  - For a HEAD, and for a GET of an empty object, `RGWGetObj::execute`
    calls `send_response_data(bl, 0, 0)` and drops its return
    (`rgw_op.cc:2436-2439`, `:2668-2671`), so the refusal goes nowhere.
    beast's client writes the status line and the length into its send
    buffer (`ClientIO::send_status` and `send_content_length`,
    `rgw_asio_client.cc:109-118` and `:183-192`), the reordering filter
    holds the other headers until `complete_header`
    (`rgw_client_io_filters.h:376-445`), and the buffer is flushed only by
    `complete_header` (`rgw_asio_client.cc:143-164`). Nothing calls it:
    the buffering filter's `complete_request` does only when no length was
    sent (`rgw_client_io_filters.h:222-253`), and `ClientIO::complete_request`
    flushes nothing (`rgw_asio_client.cc:97-102`). The `StreamIO` and its
    buffer belong to the one request (`rgw_asio_frontend.cc:317-318`), so
    by code reading the client gets no response: it waits for one that does
    not come, and one that pipelines takes the next response for this one.
  - For any other GET the refusal comes in the first data callback
    (`RGWGetObj::get_data_cb`, `rgw_op.cc:2155-2159`, `:2390-2395`), fails
    the read, and execute's error path calls `send_response_data_error`
    (`:2461`, `:2693`). That runs `send_response_data` again with
    `sent_header` still false, so it sends a second status line, the 400,
    behind the 200 or 206 status line and length already in the buffer.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01, where
  execute still drops the return at `rgw_op.cc:3035-3042`).
- **rgw-go:** answers 400 InvalidRequest in every case;
  `docs/exclusions.md` records the difference.
- **Upstream:** none. A prior-art search on 2026-10-02 found no issue or fix
  PR; it is unfiled while filing is paused. A fix would run the anonymous
  and control-character checks before `dump_errno` sends the status.
- **Found:** review of phase 1 unit R, Task 5, 2026-10-02; derived from the
  source, not reproduced.

## radosgw terminates on a stored lz4 block too short for its pair table

- **Kind:** defect. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's, and a single one holds at both.
  - `LZ4Compressor::decompress` reads a stored block's `count`, then
    `count` pairs of lengths, with ceph's `decode`
    (`compressor/lz4/LZ4Compressor.cc:110-116`). Past the end of its input
    that decode throws `buffer::end_of_buffer`: at once when nothing is
    left (`include/denc.h:1695-1696`, `:1694-1695`), and otherwise when the
    integer runs past the bytes that are (`ptr::iterator_impl::operator+=`,
    `common/buffer.cc:585-590`, `:634-639`). So a block shorter than four
    bytes, or than four plus eight per pair its count announces, throws
    instead of returning an error.
  - Between the two decodes, `std::vector<std::pair<uint32_t, uint32_t>>
    compressed_pairs(count)` value-initialises `count` pairs
    (`compressor/lz4/LZ4Compressor.cc:111`), eight bytes each: 32 GiB for a
    four-byte block of `ff ff ff ff`, allocated and zero-filled before the
    first pair's decode throws. Where that allocation fails it throws
    `std::bad_alloc`, which nothing catches either; where it succeeds the
    process may exhaust the host's memory first.
  - `RGWGetObj_Decompress::handle_data` calls it for every whole block of a
    GET with no handler around the call (`rgw/rgw_compression.cc:148`). It
    runs from `get_obj_data::flush` within `RGWRados::iterate_obj`
    (`rgw/driver/rados/rgw_rados.cc:7345-7379`, `:8196-8230`), under
    `RGWGetObj::execute`'s `read_op->iterate` (`rgw/rgw_op.cc:2443`,
    `:2675`).
  - Nothing above catches it. `rgw_process_authenticated` calls
    `op->execute(y)` bare (`rgw/rgw_process.cc:255`, `:258`), and
    `process_request` catches only `ceph::crypto::DigestException`
    (`:410`, `:417`). The beast frontend's connection coroutine rethrows
    whatever escapes it from its completion handler
    (`rgw/rgw_asio_frontend.cc:1204` and `:1221`, `:1117` and `:1134`).
    That handler runs inside `io_context::run()` on an `io_context_pool`
    thread, which catches nothing (`common/async/context_pool.h:69` and
    `:84`, `:68` and `:83`; `make_named_thread`, `common/Thread.h:73-82`),
    so the exception leaves the thread and `std::terminate` ends the
    process.
- **Impact:** a GET that reads a stored lz4 block of fewer than four bytes,
  or one whose pair table is cut short, ends the radosgw process and every
  request it is serving, after allocating up to 32 GiB on the way for a
  large count. A block whose pair table is whole but lies corrupts
  memory instead ("radosgw's LZ4 decompress trusts its block's pair table",
  above).
- **Reachability:** as for that entry. radosgw's own compressor never
  writes such a block, and an S3 or Swift client cannot supply a stored
  block's bytes; it takes a writer with access to the data pool.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01, where
  the decode, `process_request`'s one catch and the frontend's rethrow are
  unchanged).
- **rgw-go:** not affected. `internal/compression`'s lz4 decoder checks the
  block's length against its count and pair table before it reads or
  allocates anything, and the driver fails the read with 500 UnknownError;
  `docs/exclusions.md` records the difference.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit R, Task 4, 2026-10-02, checking whether radosgw
  refuses the short lz4 blocks rgw-go's decoder specs refuse; derived from
  the source, not reproduced.

## Tentacle's radosgw answers EIO for an index shard its header read means to skip

- **Kind:** defect, new in Tentacle. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`.
  - Tentacle's `cls_bucket_head` reads each shard's omap header with
    `omap_get_header` into a buffer that `IndexHeadReader::prepare_read`
    creates for the shard, and `IndexHeadReader::on_complete` takes ENOENT
    for success under the comment "ignore ENOENT"
    (`rgw/services/svc_bi_rados.cc:331-355` at v20.2.4).
  - It then decodes every buffer as an `rgw_bucket_dir_header` and answers
    -EIO when one does not decode (`:396-407`). A missing shard leaves its
    buffer empty, and so does a shard object without an omap header. An
    empty buffer does not decode, so both answer -EIO, and the ENOENT the
    reader means to skip never reaches a caller.
  - Squid's `cls_bucket_head` reads the headers through cls_rgw's
    `bucket_list` of no entries (`rgw/services/svc_bi_rados.cc:323-352` at
    v19.2.6; `CLSRGWIssueGetDirHeader`, `cls/rgw/cls_rgw_client.cc:720-728`).
    The OSD answers a read op on a missing object with ENOENT, and the
    class reads a header-less shard as the empty header
    (`read_bucket_header`, `cls/rgw/cls_rgw.cc:464-485` at v19.2.6,
    `:514-535` at v20.2.4).
  - Every bucket stats read goes through `cls_bucket_head` (`read_stats`,
    `rgw/services/svc_bi_rados.cc:395-427` at v19.2.6, `:560-592` at
    v20.2.4). The stats of an owner's bucket listing skip an ENOENT from it
    (`rgw/driver/rados/rgw_sal_rados.cc:149-155` at v19.2.6, `:158-164` at
    v20.2.4).
- **Impact:** on Tentacle, a bucket with a missing index shard fails every
  stats read with -EIO instead of ENOENT, and a Swift account listing with
  stats, which Squid completes past such a bucket, fails. A shard object
  without a header, which radosgw never writes since `bucket_init_index`
  gives every shard one, fails the same way where Squid reads it as empty.
- **Releases:** v20.2.4; v19.2.6 reads through `bucket_list`.
- **rgw-go:** the driver's `readShardHeaders` (`internal/driver/index.go`)
  reads the headers through `bucket_list`, as Squid does, on both releases
  (`docs/exclusions.md`, "Index shard headers are read as Squid reads
  them").
- **Upstream:** none. A prior-art search on 2026-10-04 found no tracker
  issue, and no fix: `IndexHeadReader`'s history holds only the commit that
  introduced it. That commit, e4fd504e3ff ("rgw/rados: index operations use
  async_reads/writes()", 2024-11-07), merged with
  [ceph/ceph#60670](https://github.com/ceph/ceph/pull/60670), brought the
  regression. It is first in v20.1.0, and no v19 release carries it, so
  v19.2.6 is not affected. It is unfiled while filing is paused, and a
  report of the Tentacle defect waits on a reproduction on a running
  cluster.
- **Found:** phase 1 metadata plane (unit M, Task 6), 2026-10-02,
  implementing the RADOS driver's index stats; derived from the source, not
  reproduced.

## cls_user reset_user_stats2 drops the stats of every page but the last

- **Kind:** defect. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`; the lines are the same at v19.2.6
  and v20.2.4. On ceph main at 06adccc25d6 (2026-10-01) the defect is
  unchanged: the class body, `acc_stats` and `update_call` are as at
  v20.2.4. Since v20.2.4, main has changed in `cls/user/cls_user.cc`,
  `cls/user/cls_user_ops.h`, `cls/user/cls_user_types.h` and
  `rgw/driver/rados/buckets.cc` only their modelines, every
  `generate_test_instances` signature in the two headers, and how the
  class's methods are named: new `cls::user::method` constants at the end
  of `cls_user_ops.h`, which the class's registration and
  `buckets::reset_stats` now use. It also adds an include at
  `cls/user/cls_user_ops.h:8`, so the `:187-199` cited below is
  `:188-200` on main; the other lines cited in these four files are the
  same there.
  - `cls_user_reset_stats2` sums one page of at most 1000 bucket entries
    after the request's marker into a `cls_user_reset_stats2_ret` it
    constructs for the call, whose `acc_stats` start at zero (`:459`,
    `:482`), and on the last page writes a fresh header holding that sum
    (`cls/user/cls_user.cc:444-506`). It never reads the request's
    `acc_stats`.
  - radosgw's `buckets::reset_stats` sends each page's marker and summed
    stats on in the next request through `update_call`
    (`rgw/driver/rados/buckets.cc:223-259`;
    `cls/user/cls_user_ops.h:187-199`). The commit that introduced the
    method, 25a82ed3795 (2020-04-30, for tracker #41080), states the
    intent: it "sets new stats via progressive calls with an accumulator".
  - For an owner with more than 1000 bucket entries the header therefore
    ends holding the stats of the last page alone.
- **Impact:**
  - `radosgw-admin user stats --reset-stats` (`rgw/rgw_admin.cc:9050-9067`
    at v19.2.6, `rgw/radosgw-admin/radosgw-admin.cc:9579` at v20.2.4) and
    `radosgw-admin account stats --reset-stats` (`rgw/rgw_admin.cc:11579`
    at v19.2.6, `rgw/radosgw-admin/radosgw-admin.cc:12256` at v20.2.4,
    reaching `rgw/rgw_account.cc:538-543`) leave an owner of more than 1000
    buckets with a header short by the earlier pages' stats as they stood
    at the reset. No S3, IAM or admin API request reaches the reset.
  - The header is what radosgw's owner stats and owner quota read
    (`RadosStore::load_stats`, `rgw/driver/rados/rgw_sal_rados.cc:1256-1266`
    at v19.2.6, `:1795` at v20.2.4; `rgw/rgw_quota.cc:569` and `:630` at
    v19.2.6, `:590` and `:651` at v20.2.4).
  - Every later update moves the header by a difference and keeps the
    shortfall: a `--sync-stats`, the quota thread's own owner sync
    (`RGWOwnerStatsCache::sync_owner` calls `rgw_sync_all_stats`,
    `rgw/rgw_quota.cc:656` at v19.2.6, `:677` at v20.2.4;
    `rgw/rgw_user.cc:16-58` at v19.2.6), `set_buckets_info` and
    `remove_bucket`. Unlinking a bucket from an earlier page subtracts
    stats the header never held.
  - The counters are `uint64_t` (`cls/user/cls_user_types.h:161-164`).
    Once the owner's usage falls below the shortfall they wrap to near
    2^64, and an owner with a size quota is then refused every write
    smaller than the remaining shortfall: the quota adds the write to the
    wrapped size, and only a write at least that large wraps the sum back
    below the limit (`rgw/rgw_quota.cc:780-787` at v19.2.6, `:804` at
    v20.2.4). The object-count quota wraps the same way.
  - The shortfall stays until a reset of an owner of at most 1000 buckets.
    A user under the default `rgw_user_max_buckets` of 1000
    (`common/options/rgw.yaml.in:2394-2401` at v19.2.6, `:2494-2501` at
    v20.2.4) cannot reach the threshold.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** the driver does not reset an owner's stats in this phase.
  fakerados's user class emulator (`internal/testutil/fakerados/cls_user.go`)
  sums each page from zero as the class does, so specs see radosgw's
  result.
- **Upstream:** none. A prior-art search on 2026-10-04 found no report and
  no fix. The defect dates from 25a82ed3795 (2020), which introduced the
  method for [#41080](https://tracker.ceph.com/issues/41080), the Feature
  issue behind its design, not a report of this defect.
  [#46400](https://tracker.ceph.com/issues/46400) concerns `--sync-stats`
  alone and was fixed by
  [ceph/ceph#36542](https://github.com/ceph/ceph/pull/36542), and
  [#48327](https://tracker.ceph.com/issues/48327) and
  [#51786](https://tracker.ceph.com/issues/51786) are distinct. The open
  feature PR [ceph/ceph#66501](https://github.com/ceph/ceph/pull/66501)
  touches the code in part: it reads the request's `acc_stats` on the final
  page only. It is unfiled while filing is paused; a report waits on a
  cluster reproduction and must first check main against ceph/ceph#66501.
- **Found:** phase 1 metadata plane (unit M, Task 6), 2026-10-02, writing
  the cls_user emulator; derived from the source, not reproduced.

## Squid's negated condition operators hold when any pair differs, and its Null ignores its values

- **Kind:** defect, fixed in v20.2.3 and unfixed on squid. Unreproduced:
  derived from the source.
- **Evidence:** paths are under `src/rgw/`.
  - At v19.2.6, `StringNotEquals`, `StringNotEqualsIgnoreCase`,
    `StringNotLike`, `ArnNotEquals` and `ArnNotLike` evaluate
    `orrible(std::not_fn(f))` over every pair of a value the key holds and
    a condition value, and `NumericNotEquals` and `DateNotEquals` evaluate
    `shortible(std::not_fn(equal_to))` (`rgw_iam_policy.cc:890-892`,
    `:898-899`, `:905-906`, `:921-923`, `:943-945` and `:999-1001`; the
    helpers at `rgw_iam_policy.h:482-514`). Each holds when some pair
    differs, so `StringNotEquals` with the values `alice` and `bob` holds
    for the user `bob`, who differs from `alice`, and a negated operator on
    a key holding two values holds whenever one of them differs. AWS's
    negated operators hold when the key's value matches none of the
    condition's.
  - `Null` answers whether the key is absent and ignores its values
    (`rgw_iam_policy.cc:857-859`), so `"Null": {"k": "false"}`, which asks
    that the key be present, holds only when it is absent.
  - v20.2.4 evaluates the negated operators as `multimap_none` and
    `typed_none`, which hold when no pair matches
    (`rgw_iam_policy.cc:910-912`, `:918-919`, `:925-926`, `:941-943`,
    `:964-966` and `:1003-1005`; `rgw_iam_policy.h:514-525` and
    `:548-567`), and compares `Null`'s values, through `as_bool`, with
    whether the key is absent (`rgw_iam_policy.cc:876-879`). Its
    `ConditionTest` cases pin both (`test/rgw/test_rgw_iam_policy.cc`). The
    changes are 225241d6ae6 ("rgw/iam: fix NotEquals conditions to use AND
    logic instead of OR", 2025-09-20; tentacle cherry-pick a3732175a7c) and
    1d0c8c286cc ("rgw/iam: match value of Null condition", 2026-02-03;
    tentacle cherry-pick ec9e7445814). `NotIpAddress` already held only
    when no pair matched at v19.2.6 (`rgw_iam_policy.cc:974-992`).
- **Impact:** on Squid, an Allow guarded by a negated operator grants a
  request whose value the condition lists, once the condition lists another
  value or the key holds another; a Deny so guarded denies it. A `Null`
  condition that asks for the key's presence holds exactly when the key is
  absent.
- **Releases:** v19.2.6 and the squid branch head (a742f50616e,
  2026-09-03), and v20.2.0 through v20.2.2; v20.2.3 and v20.2.4 carry both
  changes. Older squid releases not checked.
- **rgw-go:** follows the zone's release: `policy.Semantics`'s
  `NotMeansNone` and `NullTestsValues` select v19.2.6's rule on Squid and
  v20.2.4's on Tentacle (`internal/policy/condition.go`).
- **Upstream:** fixed upstream and tracked; both first shipped in a stable
  release in v20.2.3, and main carries them from the development tags
  v21.0.0 (NotEquals) and v21.0.1 (Null).
  - NotEquals: [#73146](https://tracker.ceph.com/issues/73146), fixed on
    main by 225241d6ae6 ([ceph/ceph#65606](https://github.com/ceph/ceph/pull/65606),
    merged 2025-10-06) and backported to tentacle as a3732175a7c
    ([ceph/ceph#67214](https://github.com/ceph/ceph/pull/67214)); the squid
    backport [ceph/ceph#67213](https://github.com/ceph/ceph/pull/67213) is open.
  - Null: [#74736](https://tracker.ceph.com/issues/74736), fixed on main by
    1d0c8c286cc ([ceph/ceph#67188](https://github.com/ceph/ceph/pull/67188),
    merged 2026-04-17) and backported to tentacle as ec9e7445814
    ([ceph/ceph#68444](https://github.com/ceph/ceph/pull/68444)); the squid
    backport [ceph/ceph#68445](https://github.com/ceph/ceph/pull/68445) is open.
- **Found:** phase 1 unit Z, Task 3, 2026-10-02, transcribing
  `Condition::eval` at both tags; derived from the source, not reproduced.

## radosgw's date conditions wrap past 2554 and before 1970

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source, with the conversions compiled and run.
- **Evidence:** paths are under `src/`; each pair of lines below is
  v19.2.6's, then v20.2.4's.
  - `Condition::as_date` converts a date condition's values and the
    environment's value to `ceph::real_time` (`rgw/rgw_iam_policy.h:384-401`,
    `:403-420`), a count of unsigned 64-bit nanoseconds since the epoch
    (`common/ceph_time.h:66` and `:74`, `:63` and `:71`), and the date
    operators compare those counts (`rgw/rgw_iam_policy.cc:940-959`,
    `:961-979`).
  - A value `std::stod` converts whole is a count of seconds; its whole
    seconds and its fraction are each cast to `uint64_t` and summed in
    nanoseconds (`rgw/rgw_iam_policy.h:390-394`, `:409-413`). The seconds
    cast is undefined behaviour for a count of -1 or less, NaN, or 2^64
    and more, and the fraction cast for any negative count, NaN, or 2^64
    and more; x86-64 code from GCC 11.5, 13.5 and 15.3 alike gives 2^63 -
    10^9 nanoseconds for `-1`, which compares as
    2262-04-11T23:47:15.854775808Z. The seconds-to-nanoseconds multiply
    and the sum are signed int64 (`std::chrono::nanoseconds`), which
    overflows, also undefined, for a count from about 9.2e9 seconds, year
    2262, up to 2^64, and on x86-64 for NaN and for a count of -1 or less,
    whose casts there give the integer indefinite. A count between -1 and
    0, such as -0.5, overflows nothing and reaches only the fraction cast.
    GCC's imul and add wrap two's-complement, as unsigned arithmetic does,
    so only the casts could differ on another architecture.
  - Any other value goes to `from_iso_8601` (`common/iso_8601.cc:51-151`
    at both tags), whose seconds `real_clock::from_time_t` converts to
    nanoseconds modulo 2^64 (`common/ceph_time.h:149-151`, `:146-148`), so
    an instant past 2554-07-21T23:34:33.709551615Z wraps:
    `2600-01-01T00:00:00Z` compares as 2015-06-13T00:25:26.290448384Z. It
    checks the range of no field but the year, and a month or day of `00`
    in 1970 gives an instant before the epoch, which wraps the other way:
    `1970-01-00` compares as 2554-07-20T23:34:33.709551616Z.
- **Impact:** `{"DateLessThan": {"aws:CurrentTime":
  "2600-01-01T00:00:00Z"}}`, meant to hold for centuries, does not hold
  today; the epoch date `-1` compares as 2262, and `-0.5` as 2554.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01,
  `rgw/rgw_iam_policy.h:488`); older releases not checked.
- **rgw-go:** reproduces it, the undefined cast as radosgw's x86-64 build
  computes it (`policy.AsDate`); `docs/exclusions.md` records that a build
  for another architecture may answer otherwise.
- **Upstream:** none. A prior-art search on 2026-10-04 found no tracker
  issue; the nearest, [#56993](https://tracker.ceph.com/issues/56993), an
  overflow in object lock's retention date,
  [#74398](https://tracker.ceph.com/issues/74398),
  [#18828](https://tracker.ceph.com/issues/18828),
  [#18829](https://tracker.ceph.com/issues/18829) and
  [#12863](https://tracker.ceph.com/issues/12863), are distinct. No fix has
  merged: `as_date` is as 69a5eebd8a4 ("rgw: Add basic support for IAM
  policies", 2016) introduced it but for 268f75bb8d3's switch to
  `std::string_view` (2020). It is unfiled while filing is paused, and a
  report waits on a cluster reproduction.
- **Found:** phase 1 unit Z, Task 3, 2026-10-02, transcribing `as_date`. A
  verbatim transcription of `as_date`, `from_iso_8601` and
  `internal_timegm`, compiled with GCC 15.3, agrees with rgw-go on 43 date
  inputs, and its double-to-integer arithmetic gives the same answers under
  GCC 11.5 and 13.5; not reproduced on a running radosgw.

## radosgw never expires a presigned SigV4 URL dated before 1970

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's, and a single line is the same at both.
  - `parse_v4_query_string` reads X-Amz-Date with `parse_iso8601`, whose
    strptime `%Y` takes any year up to 9999 and which checks no range
    (`rgw_common.cc:599-659`, `:612-672`). It then computes
    `uint64_t req_sec = (uint64_t)internal_timegm(&date_t)` and refuses the
    URL as expired when `now >= req_sec + exp`, all in `uint64_t`
    (`rgw_auth_s3.cc:314-316`, `:317-319`).
  - `internal_timegm` returns a signed `time_t`, negative for a date before
    1970 (`include/timegm.h:54-76`). The cast wraps it to just under 2^64.
    For a date more than X-Amz-Expires seconds before
    1970-01-01T00:00:00Z the sum stays above every clock, so the URL never
    expires; for a date less than that before it, the sum wraps below
    X-Amz-Expires, so the URL is already expired. `19691231T000000Z` with an
    X-Amz-Expires of 300 is therefore valid whenever it is used.
  - `now` is `ceph_clock_now()` converted to `uint64_t` through utime_t's
    `operator double` (`include/utime.h:230-232`), which adds the
    nanoseconds in a double before the truncation to seconds. So a time a
    fraction of a microsecond short of the next second counts as that
    second, and an expiry lands up to about 120 ns early, which no client
    can observe.
- **Impact:** a presigned URL dated before 1970 by more than its expiry is
  accepted at any time, though its date and expiry say it has expired. The
  date is signed, so only the holder of the secret can make such a URL, and
  radosgw applies no skew check to a presigned URL, so that holder can
  equally sign one dated in the future that is valid now and stays valid
  until then. The wrap therefore grants nothing a signer cannot already
  mint; it is not security-relevant.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01,
  `rgw_auth_s3.cc:318-320`).
- **rgw-go:** `parseV4Query` (`internal/auth/sigv4.go`) mirrors it, the
  clock's rounding included, so a presigned URL expires when radosgw's
  does.
- **Upstream:** none. A prior-art search on 2026-10-04 found no tracker
  issue; the nearest, [#76615](https://tracker.ceph.com/issues/76615),
  [#68300](https://tracker.ceph.com/issues/68300),
  [#18828](https://tracker.ceph.com/issues/18828),
  [#18829](https://tracker.ceph.com/issues/18829) and
  [#12863](https://tracker.ceph.com/issues/12863), are distinct. The code's
  history shows no fix; GitHub's pull-request search proved unreliable, so
  the history stood in for it. It is unfiled while filing is paused, and a
  report waits on a reproduction on a running radosgw.
- **Found:** phase 1 unit A, Task 4, 2026-10-02, transcribing
  `parse_v4_query_string`; derived from the source, not reproduced.

## radosgw does not check aws-chunked framing against x-amz-decoded-content-length

- **Kind:** defect. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; the code is the same at v19.2.6
  and v20.2.4, and each pair of lines is v19.2.6's, then v20.2.4's.
  - PutObject and UploadPart ask the aws-chunked decoder for
    `x-amz-decoded-content-length` bytes, in reads of at most
    `rgw_max_chunk_size` (`RGWPutObj_ObjStore::get_data`,
    `rgw_rest.cc:1068-1101`, `:1073-1106`), the completer having put that
    header in `s->length` (`rgw_auth_s3.cc:1532-1548`, `:1509-1525`). A
    read that returns nothing calls `complete()`
    (`RGWPutObj_ObjStore_S3::get_data`, `rgw_rest_s3.cc:2706-2717`,
    `:2867-2878`), and so does the read for the 0 bytes left once the
    length is delivered.
  - The decoder knows no length. It parses chunks for as long as it is
    asked for data, takes a zero-size chunk for an empty one, and verifies
    a chunk's signature when a read begins past it
    (`AWSv4ComplMulti::recv_chunk`, `rgw_auth_s3.cc:1275-1389`,
    `:1252-1366`). `complete()` verifies the chunk in progress and takes
    whatever follows it for the trailer section, from which it uses only a
    `chunk-signature=` value, the announced trailers and the trailer
    signature (`:1555-1694`, `:1532-1671`).
  - Chunks that carry more data than the length: the chunk in progress is
    verified over the bytes delivered, so a length inside a signed chunk
    fails with 400 XAmzContentSHA256Mismatch. A length at a chunk boundary,
    or any length on an unsigned payload, passes: the rest of the stream,
    data chunks included, is read as the trailer section, up to 255 bytes
    ("radosgw truncates a long aws-chunked trailer section instead of
    rejecting it"), and dropped, and the upload is stored at the length
    with 200. A signed trailer then fails with 403, because `complete()`
    chains the final chunk signature from the last chunk it verified
    rather than the client's last. PutObject then compares a supplied
    Content-MD5 with the shorter object and answers 400 BadDigest
    (`rgw_op.cc:4483-4486`, `:4715-4718`), and on v20.2.4 a trailing
    checksum found in those 255 bytes the same way (`rgw_op.cc:4757-4790`
    at v20.2.4).
  - Chunks that carry less: the final chunk is parsed as an empty one.
    When the op's read goes on, the decoder verifies that chunk's signature
    (403 SignatureDoesNotMatch on a mismatch) and fails to parse what
    follows as a chunk header (400 InvalidArgument). When the read that
    parsed it returns nothing, which needs either no data or data that
    ends where a read of `rgw_max_chunk_size` bytes ends, and a rest of the
    stream that fits the 101-byte header buffer, `complete()` verifies the
    final chunk instead (400 XAmzContentSHA256Mismatch on a mismatch), and
    PutObject's length check answers 400 RequestTimeout
    (`rgw_op.cc:4430-4433`, `:4662-4665`).
  - An empty chunk followed by more data is read past, so an upload whose
    chunks around it carry the length is stored.
- **Impact:** an upload whose `x-amz-decoded-content-length` falls short of
  its chunks is stored at that length with 200 when the length falls on a
  chunk boundary or the payload is unsigned, and no trailer, Content-MD5
  or checksum check fails; the chunks past it are dropped unread or
  unverified. One whose length exceeds its chunks is answered by where
  radosgw's reads fall.
- **Releases:** checked at v19.2.6 and v20.2.4.
- **rgw-go:** the aws-chunked reader (`internal/auth/chunked.go`) reads the
  same length and verifies the chunk in progress there as `complete()`
  does. When more data or the end of the body follows that chunk, it
  refuses a payload that expects a trailer signature with 403
  SignatureDoesNotMatch, and ends any other there, as radosgw's
  `complete()` does, leaving the rest of the body unread. A Content-MD5 is
  the op's to compare, and phase 1 compares no trailing checksum. A
  payload short of its length is answered as radosgw answers it when the
  op's read goes on (`docs/exclusions.md`, "aws-chunked framing is read
  strictly").
- **Upstream:** none. A prior-art search on 2026-10-04 found no report or
  fix PR. Related: [#81122](https://tracker.ceph.com/issues/81122) and
  [#81123](https://tracker.ceph.com/issues/81123), the narrower trailer and
  chunk-size defects recorded above, which a report of this one would
  cross-link. It is unfiled while filing is paused, and a report waits on a
  cluster reproduction.
- **Found:** phase 1 authentication (unit A, Task 6), 2026-10-02,
  implementing the aws-chunked reader; derived from the source, not
  reproduced.

## radosgw answers GetObjectTagging with 200 and no body when the tags do not decode

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's, and a single one holds at both.
  - `RGWGetObjTags::execute` reads the object's attrs and hands its
    `user.rgw.x-amz-tagging` attr to `send_response_data`
    (`rgw_op.cc:1057-1074`, `:1256-1273`).
    `RGWGetObjTags_ObjStore_S3::send_response_data` calls `dump_errno`, which
    sends the success status, and `end_header` before it decodes the tag set
    (`rgw_rest_s3.cc:746-773`, `:829-856`). When the decode throws, because
    the attr is neither an `RGWObjTags` nor the URL-encoded text
    `RGWObjTags::decode` falls back to, it sets `op_ret = -EIO` and returns
    (`:760-766`, `:843-849`).
  - `end_header` sends no Content-Length by default (`NO_CONTENT_LENGTH`,
    `rgw_rest.h:699`, `:711`), calls `complete_header` and flushes the
    formatter, which holds nothing yet (`rgw_rest.cc:589-655`, `:594-660`).
    `dump_start` then puts the XML declaration in the formatter
    (`:571-577`, `:576-582`), and the open `Tagging` and `TagSet` sections
    follow it there. Nothing flushes them: the op has no `send_response` of
    its own (`rgw_op.h:498`, `:559`), so `complete` calls `RGWOp`'s empty one
    (`:292-295`, `:306-309`), and `process_request` goes on to
    `complete_request` (`rgw_process.cc:260` and `:454`, `:263` and `:461`).
  - With no Content-Length sent, the buffering filter holds the response,
    and `complete_request` sends the length of what it holds, 0
    (`BufferingFilter`, `rgw_client_io_filters.h:208-252`).
  - So by code reading the client gets 200, a Content-Type of
    `application/xml`, a Content-Length of 0 and no body.
  - `RGWGetBucketTags_ObjStore_S3::send_response_data` renders a bucket's
    tag set the same way and fails the same way (`rgw_rest_s3.cc:839-866`,
    `:921-948`).
- **Impact:** a client reading the tags of an object, or of a bucket, whose
  tag attr does not decode gets an empty 200, which an S3 SDK cannot parse
  as a Tagging document, instead of an error. radosgw's own writes store a
  tag set that decodes, so only a corrupted attr, or one set by a writer
  with access to the data pool, meets it.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01,
  `rgw_rest_s3.cc:863` and `:955`, the -EIO at `:881` and `:973`).
- **rgw-go:** `op.GetObjectTagging` and `op.GetBucketTagging` answer 500
  UnknownError, the status radosgw's -EIO maps to; `docs/exclusions.md`
  records the difference.
- **Upstream:** none for this defect. A prior-art search on 2026-10-04
  found no report or fix of the response ordering. Its symptom was reported
  as [#74917](https://tracker.ceph.com/issues/74917), now resolved, for
  multipart objects whose tags radosgw had stored as URL-encoded text, and
  fixed as a decoder too strict:
  [ceph/ceph#67336](https://github.com/ceph/ceph/pull/67336) (main), with
  [ceph/ceph#67926](https://github.com/ceph/ceph/pull/67926) (squid, in
  v19.2.5) and [ceph/ceph#67927](https://github.com/ceph/ceph/pull/67927)
  (tentacle, in v20.2.2), added the fallback to that text and changed only
  `rgw_tag.h`. An attr that decodes neither way still gets the empty 200
  at v19.2.6 and v20.2.4. A report must distinguish this defect from
  #74917 and ceph/ceph#67336. It is unfiled while filing is paused.
- **Found:** phase 1 unit R, Task 6, 2026-10-02, implementing
  GetObjectTagging; derived from the source, not reproduced.

## radosgw counts and pages a multipart object's parts as if their numbers had no gaps

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/driver/rados/`; each pair of lines
  is v19.2.6's, then v20.2.4's, and a single one holds at both or, where
  marked, at v20.2.4 alone.
  - A multipart upload's part numbers may have gaps. CompleteMultipartUpload
    must name every part uploaded, in order, but not every number
    (`RadosMultipartUpload::complete`, `rgw_sal_rados.cc:3471` and `:3488`,
    `:4319` and `:4336`). Each part's manifest carries its number as its
    rule's `start_part_num` (`MultipartObjectProcessor::prepare_head`,
    `rgw_putobj_processor.cc:455`, `:489`), and `RGWObjManifest::append`
    starts a new rule wherever the next part's number is not the one it
    expects (`rgw_obj_manifest.cc:106-114`). Parts 1 and 3 therefore keep
    rules for parts 1 and 3, and the iterator steps from part 1 to part 3.
  - The parts count is `obj_end`'s part id less one (`get_part_obj_state`,
    `rgw_rados.cc:6778`, `:7614`; `Read::prepare`, `:7714` at v20.2.4).
    `obj_end` lies one past the last part's number, so the count is the
    last part's number: 3 for parts 1 and 3. GET and HEAD send it as
    `x-amz-mp-parts-count`, with `partNumber` at v19.2.6 and for every
    multipart object at v20.2.4 (`rgw_rest_s3.cc:496`, `:531`), and
    GetObjectAttributes as PartsCount and TotalPartsCount
    (`rgw_rest_s3.cc:4105-4106` at v20.2.4).
  - GetObjectAttributes's ObjectParts, at v20.2.4 alone, pages through
    `RadosObject::list_parts` (`rgw_sal_rados.cc:2834-2933`). A marker other
    than 0 resumes at `obj_find_part(marker + 1)`, which finds that part
    number exactly or nothing (`rgw_obj_manifest.cc:200-219`), and finding
    nothing lists nothing (`rgw_sal_rados.cc:2861-2874`). Each part listed
    moves the next marker on by one from the marker, not to the part's
    number (`:2928`).
  - So, paging with max-parts 1, parts 1 and 3 list as part 1 with
    NextPartNumberMarker 1, then nothing for marker 1, and part 3 is never
    listed; parts 2 and 3 list as part 2 with marker 1, part 2 again with
    marker 2, then part 3.
- **Impact:** for a multipart object whose part numbers have gaps, every
  client reads a parts count above the number of parts, and a client paging
  GetObjectAttributes's ObjectParts on Tentacle misses parts or sees one
  twice. An object whose parts are numbered from 1 without gaps, which S3
  SDKs' uploaders write, is unaffected.
- **Releases:** v19.2.6 and v20.2.4 for the count, v20.2.4 for the paging,
  and main for both (06adccc25d6, 2026-10-01, `rgw_rados.cc:8226` and
  `:8326`, `rgw_sal_rados.cc:3046` and `:3108`).
- **rgw-go:** reproduces both. `meta.Manifest.PartsCount` is radosgw's
  count, and `op.GetObjectAttributes` resumes and counts its pages as
  `list_parts` does, so rgw-go answers as radosgw does.
- **Upstream:** none. A prior-art search on 2026-10-04 found no report or
  fix PR. The nearest report,
  [#68427](https://tracker.ceph.com/issues/68427), is a different defect,
  and [ceph/ceph#66764](https://github.com/ceph/ceph/pull/66764), closed
  unmerged, is no fix: it changes only when `x-amz-mp-parts-count`
  appears. The count came with partNumber support,
  [ceph/ceph#50148](https://github.com/ceph/ceph/pull/50148), and the
  paging with GetObjectAttributes,
  [ceph/ceph#55259](https://github.com/ceph/ceph/pull/55259). It is
  unfiled while filing is paused.
- **Found:** phase 1 unit R, Task 6, 2026-10-02, implementing
  GetObjectAttributes's part listing; derived from the source, not
  reproduced.

## Squid's account admin API checks a cap type that does not exist for get and delete

- **Kind:** defect, not security-relevant (it refuses), fixed in Tentacle
  from v20.2.2 and unfixed on squid. Unreproduced: derived from the source;
  go-ceph v0.39.0's rgw/admin suite skips both calls for every release up
  to tentacle.
- **Evidence:** paths are under `src/rgw/`; lines are v19.2.6's unless a
  tag is named.
  - `RGWOp_Account_Get` and `RGWOp_Account_Delete`, which serve GET and
    DELETE on the admin resource `account` (`rgw_rest_account.cc:233-241`;
    `rgw_appmain.cc:358`), check the cap `account`
    (`rgw_rest_account.cc:174`, `:196`). Create and modify check `accounts`
    (`:23`, `:107`).
  - No caps command can grant `account`: `RGWUserCaps::get_cap` refuses a
    type `is_valid_cap_type` rejects (`rgw_common.cc:1907-1914`), and that
    list holds `accounts`, not `account` (`:2083-2110`).
    `RGWUserCaps::decode_json` does not check the type (`:2059-2069`), so
    `radosgw-admin metadata put` can store one.
  - `check_cap` answers EPERM for a type the user lacks (`:2071-2081`).
    `RGWRESTOp::verify_permission` is that check (`rgw_rest.cc:1687-1690`),
    and only a system request or an admin user overrides its refusal
    (`rgw_process.cc:228-234`).
  - v20.2.0 and v20.2.1 still check `account` at `:174` and `:196`. From
    v20.2.2 every account op checks `accounts`; at v20.2.4 that is `:23`,
    `:107`, `:174`, `:196` and `:226`. origin/squid at a742f50616e still
    checks `account` and has no commit citing the fix.
- **Impact:** on Squid, GET and DELETE on `/admin/account` answer 403 to
  every user that is neither an admin nor a system user and holds no
  `account` cap stored through `metadata put`, one holding `accounts=*`
  included. Through `/admin/account` such a user can create and modify an
  account but not read or remove it. `radosgw-admin account get` and `rm`
  are unaffected.
- **Releases:** v19.2.6, v20.2.0, v20.2.1 and origin/squid (a742f50616e);
  fixed in v20.2.2 and main.
- **rgw-go:** its `/admin/account` handlers are phase 1 work not yet
  written; the task that writes them reproduces Squid's refusal or records
  the difference in `docs/exclusions.md`.
  `test/admin/baseline/squid.json` holds both calls' subtests as skipped,
  and `tentacle.json` as passed.
- **Upstream:** fixed upstream before this entry; not found by us.
  - Fixed on main by [ceph/ceph#65480](https://github.com/ceph/ceph/pull/65480)
    (187573e9e7d, merged 2025-10-22), the admin API's account-quota work,
    whose fix commit cites [#72527](https://tracker.ceph.com/issues/72527).
  - Fixed on tentacle by [ceph/ceph#66905](https://github.com/ceph/ceph/pull/66905)
    (c6b80a3b67e, merged 2026-05-04), first released in v20.2.2.
  - The squid backport,
    [ceph/ceph#66919](https://github.com/ceph/ceph/pull/66919), was open on
    2026-10-02. A squid backport,
    [ceph/ceph#71186](https://github.com/ceph/ceph/pull/71186), was closed
    unmerged, so v19.2.6 keeps the defect.
  - [#69544](https://tracker.ceph.com/issues/69544), a sibling report, is
    Resolved by the same pull requests.
- **Found:** phase 1 unit T, Task 6, 2026-10-02, deciding which release the
  go-ceph rgw/admin suite runs as; derived from the source, not reproduced.

## radosgw's eval_principal skips NotPrincipal for a role that Principal names, but not for a user

- **Kind:** defect on the squid and tentacle branches; main lost it to a
  refactor. Potentially security-relevant, under a condition: it
  over-grants only on v19.2.6 and the releases like it, to an Allow that
  names a role in both Principal and NotPrincipal. Unreproduced: derived
  from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `Statement::eval_principal` checks a non-role identity against a
    non-empty Principal, then a role identity (TYPE_ROLE) against it, and
    the NotPrincipal check is the `else if` of the role branch
    (`rgw_iam_policy.cc:1253-1273`, `:1257-1277`). A role that a non-empty
    Principal names is never checked against NotPrincipal; a user in the
    same position is.
  - The parser accepts Principal and NotPrincipal in one statement: its
    duplicate-key check covers only a repeated key (`dex`, `test` and `set`
    at `rgw_iam_policy.cc:277-329`, `:287-339`; the `!pp->test(k->id)` at
    `:504`, `:514`). v20.2.4 refuses Allow with NotPrincipal (`:771-776`),
    not Deny with both.
  - On main, f7c44ac833e ("rgw/iam: Policy::eval() returns Principal, not
    just type", 2026-01-20) checks NotPrincipal first for every identity;
    the squid (a742f50616e) and tentacle (7411a080411) branch heads keep
    the `else if`.
- **Impact:** the rule matters only for a statement that names the role in
  both Principal and NotPrincipal, which AWS does not allow. A Deny
  statement whose Principal matches a role session and whose NotPrincipal
  names that role still applies to the role, where the same exemption
  works for a user. On v19.2.6 an Allow statement naming a role in both
  lists grants it; v20.2.4 refuses that Allow when it parses the policy,
  so only a Deny reaches the rule there.
- **Releases:** v19.2.6, v20.2.4 and both branch heads above; not main.
- **rgw-go:** reproduces it in `policy.Statement.EvalPrincipal`
  (`internal/policy/statement.go`). In phase 1 no identity is a role, so
  the role rule is unreached until STS.
- **Upstream:** none. A prior-art search on 2026-10-04 found no report or
  fix PR. Main lost the rule incidentally: f7c44ac833e came with
  [ceph/ceph#66999](https://github.com/ceph/ceph/pull/66999), merged
  2026-09-25, whose issue,
  [#74471](https://tracker.ceph.com/issues/74471), concerns a resource
  policy's grant to an account principal, not this defect. It is not
  backported to squid or tentacle. It is unfiled while filing is paused,
  and a report waits on a cluster reproduction.
- **Found:** phase 1 unit Z, Task 4, 2026-10-04, transcribing
  `eval_principal`; derived from the source, not reproduced.

## radosgw's is_public judges a wildcard-principal statement against a fixed three-key environment

- **Kind:** defect, security-relevant: a public-access-block bypass with a
  common, valid policy, such as `Principal: *` under
  `aws:SecureTransport`; unfixed through main. Unreproduced: derived from
  the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `is_public` evaluates an Allow statement whose Principal holds the
    wildcard with `eval_conditions(iam_all_env)`
    (`rgw_iam_policy.cc:1904-1907`, `:1939-1942`). `iam_all_env` holds
    only aws:SourceIp `1.1.1.1`, aws:UserId `anonymous` and
    s3:x-amz-server-side-encryption-aws-kms-key-id `secret`
    (`:1894-1898`, `:1929-1933`), as it has since ff972d69567 ("rgw:
    initial implementation of a public policy tester", 2019).
  - Apart from `Null`, a condition on any other key fails unless it uses
    IfExists or a ForAllValues operator (`Condition::eval`,
    `rgw_iam_policy.cc:857-869`, `:875-889`). So a statement granting every
    principal under, say, `Bool aws:SecureTransport true`,
    `StringLike aws:Referer …` or `StringEquals s3:prefix …` is judged not
    public, although every requester meeting the condition is granted. A
    condition 1.1.1.1 meets, such as `IpAddress aws:SourceIp 1.1.1.1/32`,
    makes a statement public that admits one address.
  - The verdict decides PutBucketPolicy's BlockPublicPolicy refusal
    (`rgw_op.cc:8103-8108`, `:9029-9034`), GetBucketPolicyStatus's IsPublic
    (`:8597`, `:9578`) and, on v20.2.4 only, RestrictPublicBuckets for
    buckets and for objects (`rgw_common.cc:1377` and `:1544`, both
    v20.2.4).
- **Impact:** a bucket policy that opens a bucket to everyone behind a
  condition on any other key passes BlockPublicPolicy, is reported not
  public, and on Tentacle escapes RestrictPublicBuckets. The guardrails
  meant to catch a public policy let it through, and the data is open to
  anyone who meets the condition. It is not a privilege escalation, since
  the bucket owner writes the policy, but it defeats the block that is
  meant to stop that owner, or an administrator's account-wide setting,
  from publishing the bucket.
- **Releases:** v19.2.6, v20.2.4, and main (06adccc25d6, 2026-10-01), whose
  `IsPublicStatement` still evaluates `iam_all_env`
  (`rgw_iam_policy.cc:2174-2178` and `:2193`).
- **rgw-go:** reproduces it: `Policy.IsPublic` evaluates the statement's
  conditions through `Statement.EvalConditions` in the same three-key
  environment (`internal/policy/policy.go`).
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. A prior-art search on 2026-10-04 found no
  report or fix PR. Related, in the opposite direction: Squid's false
  positive for an Allow statement whose NotPrincipal holds no wildcard,
  reported as [#67047](https://tracker.ceph.com/issues/67047) and again as
  its duplicate [#67048](https://tracker.ceph.com/issues/67048). Its fix,
  019aaa4d101 with
  [ceph/ceph#58686](https://github.com/ceph/ceph/pull/58686), is first
  tagged in v20.0.0 and released in v20.2.0; the squid backport tracker,
  [#67176](https://tracker.ceph.com/issues/67176), has no PR yet. This
  defect is unfiled while filing is paused.
- **Found:** phase 1 unit Z, Task 4, 2026-10-04, transcribing `is_public`;
  derived from the source, not reproduced.

## radosgw's SigV2 date check wraps the request time to 32 bits

- **Kind:** defect, not security-relevant. Unreproduced: derived from the
  source.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's, and a single line is the same at both.
  - `rgw_create_s3_canonical_header` keeps a header-signed request's date
    as `utime_t(internal_timegm(&t), 0)` less `tm_gmtoff`
    (`rgw_auth_s3.cc:241-242`, `:243-244`). utime_t keeps its seconds in a
    `__u32` (`include/utime.h:51`); its `(time_t, int)` constructor stores
    the time_t there (`:72`) and `operator-=(utime_t&, double)` subtracts in
    it (`:551-554`), so the time is taken modulo 2^32.
  - `get_auth_data_v2` hands it to `is_time_skew_ok(time_t)` through
    `operator double` (`include/utime.h:230`; `rgw_rest_s3.cc:6101`,
    `:6672`).
  - A Date at or after 2106-02-07T06:28:16Z therefore wraps, and one a
    multiple of 2^32 seconds ahead of the clock, within the 15-minute grace,
    passes the skew check: at 2015-08-30T12:36:00Z,
    `Wed, 06 Oct 2151 19:04:16 GMT` reads as the current time. A 1970 date
    that its zone offset moves before the epoch passes the year check
    (`rgw_auth_s3.cc:237-240`, `:239-242`) and wraps to 2106.
- **Impact:** a header-signed SigV2 request dated a multiple of about 136
  years ahead is accepted as current. The date is signed, so only a client
  that holds the secret and signed such a date meets it.
- **Releases:** v19.2.6 and v20.2.4; main not checked.
- **rgw-go:** differs: it compares the full time and answers such a request
  with 403 RequestTimeTooSkewed (`docs/exclusions.md`).
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. The wrap is `utime_t`'s 32-bit seconds, as in "radosgw's
  parse_time wraps a date outside 1970 to 2106". It is unfiled while filing
  is paused.
- **Found:** phase 1 unit A, Task 8, 2026-10-04, transcribing SigV2's
  header time; derived from the source, not reproduced.

## radosgw signs only the last value of a repeated x-amz- header

- **Kind:** defect, not security-relevant: radosgw signs a repeated header
  otherwise than AWS's clients do. Unreproduced: derived from the source.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's, and a single line is the same at both.
  - beast files each request header with `RGWEnv::set`
    (`rgw_asio_client.cc:36-66`), which overwrites (`rgw_env.cc:22-25`), so
    of a repeated header only the last value is kept. SigV2's amz headers
    come from that environment (`req_info::init_meta_info`,
    `rgw_common.cc:422-464`, `:435-477`), and SigV4's canonical headers
    look each signed header up in it (`get_v4_canonical_headers`,
    `rgw_auth_s3.cc:734-834`, `:711-811`).
  - botocore's SigV2 signer joins all of a repeated `x-amz-` header's values
    with commas (`HmacV1Auth.canonical_custom_headers`,
    `botocore/auth.py:934-947` at 1.43.90 and 1.43.106).
- **Impact:** a request that repeats an `x-amz-` header, such as two values
  of one `x-amz-meta-` header, signed as botocore signs it, fails with 403
  SignatureDoesNotMatch.
- **Releases:** v19.2.6 and v20.2.4; main not checked.
- **rgw-go:** reproduces it, in SigV2 (`amzHeadersV2`) and in SigV4
  (`requestView.header`), both in `internal/auth`, so such a request fails
  alike.
- **Upstream:** reported before us, as
  [#75304](https://tracker.ceph.com/issues/75304), with
  [ceph/ceph#70459](https://github.com/ceph/ceph/pull/70459); not to be filed
  again.
- **Found:** phase 1 unit A's plan, which mirrors it (D-A7); registered in
  Task 8, 2026-10-04, transcribing SigV2's amz headers; derived from the
  source, not reproduced.

## radosgw's SigV2 resource lists encryption and object-lock but never signs them

- **Kind:** defect, not security-relevant. Unreproduced: derived from the
  source.
- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `signed_subresources`, the sub-resources SigV2's canonical resource
    appends when the request carries them, names `encryption` and
    `object-lock` (`rgw_auth_s3.cc:35` and `:59`, `:36` and `:60`).
  - `get_canon_resource` looks each name up among the request's
    sub-resources (`rgw_auth_s3.cc:91-124`, `:92-125`), which only
    `RGWHTTPArgs::append` files (`rgw_common.cc:951`, `:959` and `:972`;
    `:964`, `:972` and `:985`), and it files neither name
    (`rgw_common.cc:925-975`, `:938-988`). So neither is ever signed.
  - botocore's SigV2 signer signs `object-lock` (`HmacV1Auth.QSAOfInterest`,
    `botocore/auth.py:868-905`, the name at `:904`, at 1.43.90 and
    1.43.106), so for `?object-lock` it signs `/<bucket>?object-lock` where
    radosgw computes `/<bucket>`. It does not sign `encryption`.
  - The two lists differ elsewhere too: botocore signs `accelerate`,
    `restore` and `replication`, which radosgw neither lists nor files, and
    does not sign `policyStatus` and `publicAccessBlock`, which radosgw
    signs.
- **Impact:** a SigV2 request on a bucket's `?object-lock` configuration,
  signed as botocore signs it, fails with 403 SignatureDoesNotMatch.
  radosgw's `encryption` entry has no effect.
- **Releases:** v19.2.6 and v20.2.4; main not checked.
- **rgw-go:** reproduces it: `signedSubresourcesV2`
  (`internal/auth/sigv2.go`) lists both names, and the request parser files
  neither as a sub-resource, so neither is signed.
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. It is unfiled while filing is paused.
- **Found:** phase 1 unit A, Task 8's review, 2026-10-04, checking
  `signed_subresources` against `RGWHTTPArgs::append`; derived from the
  source, not reproduced.

## radosgw cuts an aws-chunked trailer section by its trailers' length, not their position

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** in `src/rgw/rgw_auth_s3.cc` unless named; each pair of
  lines is v19.2.6's, then v20.2.4's.
  - `AWSv4ComplMulti::complete` reads up to 255 bytes after the chunk at
    `x-amz-decoded-content-length` into `trailer_vec`, a
    `static_vector<char, 256>` on its stack (`:1583-1606`, `:1560-1583`).
    When more than three bytes arrived, it views them, past an optional
    `\r\n`, `0` and `;`, as `mut_sv_trailer`, and cuts the final chunk's
    `chunk-signature=` line from it (`:1624-1655`, `:1601-1632`).
  - When the request carries `x-amz-trailer`, it calls
    `extract_trailing_headers` (`:1659-1661`, `:1636-1638`). That function
    looks each name of `x-amz-trailer`, split on ",", up anywhere in the
    section with `extract_helper`, which returns the text from the name to
    the next CRLF and that text's length plus two, but not where it lies
    (`:1451-1464`, `:1428-1441`). It adds up those lengths and calls
    `mut_sv_trailer.remove_prefix(consumed)` (`:1482-1510`, `:1459-1487`),
    so it drops that many bytes from the start of the section, wherever the
    lines it found were.
  - The search for `x-amz-trailer-signature:` runs on what is left
    (`:1663-1665`, `:1640-1642`). When the drop reaches into the signature
    line, a payload that expects a trailer signature fails with 403
    SignatureDoesNotMatch though its signature is right (`:1686-1690`,
    `:1663-1667`). It does when the signature line precedes a listed
    trailer's line with fewer bytes before it than the drop; a signature
    line with at least that many bytes before it survives.
  - The lines found can repeat or overlap: a name listed twice is found
    twice, and a listed name that also occurs inside another found line,
    `crc32c` beside `x-amz-checksum-crc32c` for instance, is found there.
    Each find adds its line's length again, so the drop reaches past the
    trailers into the signature line even with the trailers first and the
    signature line last. The lengths add up to more than the section holds
    once a name is listed often enough, and
    `std::string_view::remove_prefix` with a count above `size()` is
    undefined behaviour. libstdc++ checks the count only
    under `_GLIBCXX_ASSERTIONS` (`string_view:299-304` in the system's GCC
    15 headers, not Ceph's build toolchain), which Ceph defines in Debug
    builds (`src/CMakeLists.txt:190-193`, `:208-211`), and the failed check
    aborts the process. Without it the view's length wraps to just under
    2^64, and the `find` for `x-amz-trailer-signature:` that follows reads
    past the 256-byte buffer until it finds that text or faults.
  - `get_v4_canonical_headers` requires `x-amz-trailer`, like every
    `x-amz-` header, to be signed unless `rgw_sigv4_insecure` is set
    (`:803-819`, `:780-796`).
- **Impact:** a payload that expects a trailer signature fails with 403
  SignatureDoesNotMatch, though its signature is right, when its
  `x-amz-trailer` lists a name twice or lists overlapping names, even with
  the trailers first and the signature line last, and when its signature
  line comes before a listed trailer with fewer bytes before it than the
  drop. A holder of a valid key can send an aws-chunked upload of any of
  the three streaming forms whose `x-amz-trailer` lists one name enough
  times, and reach the undefined behaviour in `complete()` at the end of
  PutObject or UploadPart: an abort that ends the radosgw process and every
  request it serves where the assertions are compiled in, a read past the
  buffer where they are not. `mut_extract_helper`, which cuts the final
  chunk's `chunk-signature=` line, drops by length in the same way: the
  match's length plus two from the start of the region, not everything up
  to the match's end (`:1442-1445`, `:1419-1422`), with no outcome beyond
  this entry's. A section as the AWS SDKs write it, the trailers first and
  the signature line last, each name listed once, is unaffected.
- **Releases:** v19.2.6, v20.2.4 and main (6cafff02b39, 2026-10-04,
  `rgw_auth_s3.cc:1501-1529`).
- **rgw-go:** not affected. `trailers` (`internal/auth/chunked.go`) reads
  the section line by line, takes each listed name once and reads nothing
  past the section, and only for a payload that expects a trailer
  signature; `docs/exclusions.md` records the difference ("aws-chunked
  trailer sections are read line by line").
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit A, Task 7, 2026-10-04, reading `complete()`'s
  trailer parse to implement rgw-go's; derived from the source, not
  reproduced.

## radosgw reads an aws-chunked trailer line's value only up to a second colon, and drops an empty one

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** in `src/rgw/rgw_auth_s3.cc` unless named; each pair of
  lines is v19.2.6's, then v20.2.4's.
  - `split_header` splits a trailer line with `ceph::split(hdr, ":")`,
    passes on its first two parts as the name and the value, and passes
    nothing when there is no second part (`:1470-1480`, `:1447-1457`).
    `ceph::split` skips empty parts (`spliterator::next`,
    `common/split.h:32-38` at both tags). So the value of `name:a:b` is
    `a`, that of `name::a` is `a`, and `name:` has none.
  - `extract_trailing_headers` puts a listed trailer into the map the
    trailer signature covers only when `split_header` passes it on
    (`:1496-1504`, `:1473-1481`), and `complete()` takes the declared
    trailer signature the same way (`:1666-1672`, `:1643-1649`).
  - `calc_v4_trailer_signature` hashes that map as `name:value\n` per
    entry (`:1391-1418`, `:1368-1395`; `get_canon_amz_hdrs`, `:66-86`,
    `:67-87`).
- **Impact:** a signed trailer whose value holds a colon or is empty fails
  with 403 SignatureDoesNotMatch when the client signs its whole value, and
  a signature line with a colon and more text after the signature is
  accepted. Checksum values are base64 and hold no colon, so the AWS SDKs
  never meet it.
- **Releases:** v19.2.6, v20.2.4 and main (6cafff02b39, 2026-10-04,
  `rgw_auth_s3.cc:1489-1499`).
- **rgw-go:** differs. `trailers` (`internal/auth/chunked.go`) splits a
  line at its first colon and takes the rest of it, untrimmed and possibly
  empty, as the value; `docs/exclusions.md` records the difference
  ("aws-chunked trailer sections are read line by line").
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit A, Task 7, 2026-10-04, reading `split_header` to
  implement rgw-go's trailer parse; derived from the source, not
  reproduced.

## radosgw's from_base64 reads before an all-'=' input

- **Kind:** defect, a denial of service: any request that reaches
  `rgw::from_base64` with an input of only '=' ends the gateway. Unfixed
  through main. Unreproduced: derived from the source, with the throwing
  call compiled and run.
- **Evidence:** paths are under `src/rgw/`; `rgw_b64.h` is the same at
  v19.2.6, v20.2.4 and main.
  - `from_base64` returns an empty string for an empty view
    (`rgw_b64.h:64-65`), then strips trailing '=' with
    `while (sview.back() == '=') sview.remove_suffix(1)` and no further
    empty check (`:76-77`). An all-'=' input such as "====" is not empty,
    so `back()` is called; once the last '=' is dropped the view is empty
    and the next `back()` reads before the start of the buffer.
  - Under `-D_GLIBCXX_ASSERTIONS` that `back()` aborts; without it, it is a
    heap out-of-bounds read. el9 RPMs compile with the flag: `ceph.spec.in`
    exports `CXXFLAGS=$RPM_OPT_FLAGS` (`:1360-1361` at v19.2.6, `:1401-1402`
    at v20.2.4) and redhat-rpm-config's optflags carry
    `-Wp,-D_GLIBCXX_ASSERTIONS` (verified on this fc43 host; el9 not
    verified here). Neither outcome is a C++ exception, so a caller's
    try/catch does not catch it, and the multi-threaded gateway ends.
  - The callers decode base64 from client input: the SSE-C customer key and
    its MD5, with the copy-source variants (`rgw_crypt.cc:1058` and `:1081`,
    `:1341` and `:1362` at v19.2.6; `:1073` and `:1096`, `:1360` and `:1382`
    at v20.2.4), the SSE-KMS encryption context (`:241`), a PutBucketLifecycle
    Content-MD5 (`rgw_op.cc:5947` at v19.2.6, `:6616` at v20.2.4), the admin
    metadata `?marker` (`rgw_rest_metadata.cc:86`), the LDAP S3 access-key id
    (`rgw_rest_s3.cc:6271` at v19.2.6, `:6842` at v20.2.4) and the STS session
    token (`:6406`, `:7014`).
  - STS and LDAP S3 auth decode before the request is authenticated, when
    `rgw_s3_auth_use_sts` or `rgw_s3_auth_use_ldap` is set (both default
    false; `rgw_auth_s3.h:91` and `rgw_rest_s3.cc:6200`); the others decode
    after authentication.
- **Impact:** with STS or LDAP S3 auth enabled, an unauthenticated request
  naming such a token ends the gateway and every request it is serving.
  Otherwise any authenticated tenant ends it with one SSE-C or lifecycle
  request, repeatably, until the request stops.
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01;
  6cafff02b39, 2026-10-04), where `from_base64` is unchanged.
- **rgw-go:** not affected. `fromBase64` of an input that is empty once its
  trailing '=' are removed returns an empty result and success
  (`internal/op/readconds.go`), so an all-'=' SSE-C key or key-MD5 is 400
  InvalidArgument, not a crash; `docs/exclusions.md` records the difference.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. A prior-art search found no report or fix;
  the 2016 LDAP non-base64 defects
  [#17544](https://tracker.ceph.com/issues/17544),
  [#17663](https://tracker.ceph.com/issues/17663),
  [#17785](https://tracker.ceph.com/issues/17785) and
  [#17324](https://tracker.ceph.com/issues/17324) are distinct. Severity
  estimate from triage: CVSS 7.5 unauthenticated with STS or LDAP enabled,
  6.5 for the authenticated floor (triage estimate). It is unfiled while
  filing is paused.
- **Found:** phase 1 unit R, Task 5, 2026-10-02, implementing the SSE-C key
  decode; derived from the source, with the throwing `back()` compiled and
  run under g++ 11.5, 13.5, 14.4 and 15.3, not reproduced on a running
  radosgw.

## radosgw reads a runtime condition's key from its last value, and terminates when that value is short

- **Kind:** defect, a denial of service: a user who can set a bucket policy
  can make radosgw terminate. Unfixed through main. Unreproduced: derived
  from the source, with the throwing call compiled and run.
- **Evidence:** paths are under `src/`; each pair of lines below is
  v19.2.6's, then v20.2.4's.
  - The policy parser marks a condition runtime when any of its values
    starts with `${` and ends with `}`, and keeps every value as written,
    numbers included (`rgw/rgw_iam_policy.cc:703-720`, `:716-732`; numbers
    at `:762-766`, `:781-785`).
  - `Condition::eval` takes the runtime key from the last value only,
    whatever it is, by erasing its first two bytes and its last
    (`rgw/rgw_iam_policy.cc:871-879`, `:891-899`). The string and ARN
    operators then compare the key's values with that key's values in place
    of the condition's (`:884-915` and `:996-1001`, `:904-935` and
    `:1000-1005`), so every other value, an interpolation among them, is
    ignored: `{"StringEquals": {"aws:username": ["${aws:username}",
    "alice"]}}` compares the user name with the values of the key `ic`.
  - For a last value shorter than three bytes the second erase,
    `k.erase(k.length() - 1, 1)`, gets `npos` as its position and throws
    `std::out_of_range`; compiled with GCC 11.5 it reports
    "basic_string::erase: __pos (which is 18446744073709551615) >
    this->size() (which is 0)". The erase runs whenever the condition's key
    is present, for every operator but `Null` (`:861-869`, `:881-889`),
    once the statement's principal, resource and action match and its
    earlier conditions hold (`Statement::eval`, `:1226-1230`,
    `:1230-1234`).
  - `is_public` reaches the same `Condition::eval` through `eval_conditions`
    against `iam_all_env`, which carries aws:SourceIp
    (`rgw/rgw_iam_policy.cc:1906`, `:1941`), for any Allow statement whose
    Principal holds the wildcard, with no gating on resource or action. Its
    callers are PutBucketPolicy with BlockPublicPolicy set, where the PUT
    itself terminates radosgw (`rgw/rgw_op.cc:8105`, `:9031`),
    GetBucketPolicyStatus (`:8597`, `:9578`) and, at v20.2.4 only,
    RestrictPublicBuckets on every non-owner request
    (`rgw/rgw_common.cc:1377` and `:1544`, both v20.2.4).
  - Nothing on the request path catches the exception. The try around the op
    in `process_request` catches only `ceph::crypto::DigestException`
    (`rgw/rgw_process.cc:410`, `:417`), the frontend's connection coroutine
    catches nothing, and its completion handler rethrows
    (`rgw/rgw_asio_frontend.cc:1204` and `:1221`, `:1117` and `:1134`) out of
    `io_context::run` on an `io_context_pool` thread, which has no handler
    either (`common/async/context_pool.h:69` and `:84`, `:68` and `:83`;
    `common/Thread.h:73-82` at both tags), so `std::terminate` ends the
    process.
- **Impact:** a bucket owner can store such a policy, which PutBucketPolicy
  accepts, and radosgw then terminates on every request the statement
  covers. A condition on a key every request carries, such as
  `aws:SourceIp`, under `"Principal": "*"` lets anyone, anonymous included,
  stop each gateway in turn; through the `is_public` path even the
  PutBucketPolicy that stores it, or any non-owner request on Tentacle, can
  do so. The same policy stores from IAM user, group and role policies, a
  role trust policy, an STS inline session policy, a pubsub topic policy and
  bucket logging. A condition that mixes an interpolation with other values
  compares with the wrong values whether or not it stops the process.
- **Releases:** v19.2.6, v20.2.4, and the squid (a742f50616e, 2026-09-03),
  tentacle (7411a080411, 2026-09-30) and main (06adccc25d6, 2026-10-01,
  `rgw/rgw_iam_policy.cc:1051-1059`) branch heads; older releases not
  checked.
- **rgw-go:** reproduces the key taken from the last value, and works
  around the termination: for a last value shorter than three bytes it
  reads the empty key, which no request sets, so the string and ARN
  operators compare with no values (`policy.Condition.Eval`).
  `docs/exclusions.md` records the difference.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. A prior-art search found no report or fix;
  it is distinct from the parse-time terminations
  [#81253](https://tracker.ceph.com/issues/81253) /
  [ceph/ceph#72271](https://github.com/ceph/ceph/pull/72271) and
  [#81248](https://tracker.ceph.com/issues/81248) /
  [ceph/ceph#72270](https://github.com/ceph/ceph/pull/72270). On main since
  5d85c65ff1af (2021), first tagged v17.1.0. Severity estimate from triage:
  CVSS 7.5 unauthenticated, 6.5 for the authenticated floor (triage
  estimate). It is unfiled while filing is paused.
- **Found:** phase 1 unit Z, Task 3, 2026-10-02, transcribing
  `Condition::eval`; derived from the source, with the throwing erase
  compiled and run, not reproduced on a running radosgw.

## radosgw spins on a ranged GET of a compressed block larger than rgw_max_chunk_size

- **Kind:** defect, a client-triggered denial of service under a config
  precondition. Unfixed through main. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's, and a single one holds at both. `rgw/rgw_compression.cc`
  is the same blob at both tags. main (06adccc25d6, 2026-10-01) changes its
  `fixup_range`, `generate_test_instances`, an include and its modeline, and
  leaves `handle_data` as it is.
  - `RGWGetObj_Decompress::handle_data` hands the decoded bytes on in a
    loop that runs while at least `rgw_max_chunk_size` of them remain past
    `q_ofs`, each pass handing on `min(rgw_max_chunk_size, q_len)` bytes and
    splicing them off (`rgw/rgw_compression.cc:154-165`). `q_len` is what
    is left of the range (`fixup_range`, `:209`).
  - Once the range is delivered `q_len` is 0: a pass hands on 0 bytes and
    splices nothing, so if `rgw_max_chunk_size` or more decoded bytes are
    still there the loop never ends. They are there when the range ends at
    least `rgw_max_chunk_size` bytes before the end of a block, so the block
    decodes to more than `rgw_max_chunk_size`.
  - The writer makes each block from one `get_data` read of at most
    `rgw_max_chunk_size` (`RGWPutObj_ObjStore::get_data`,
    `rgw/rgw_rest.cc:1071-1078`, `:1076-1083`; `RGWPutObj_Compress::process`,
    `rgw/rgw_compression.cc:44-87`), so blocks fit the value they were
    written under. The option has no minimum and no `startup` flag
    (`common/options/rgw.yaml.in:84-98`, `:84-101`), and `handle_data` reads
    it at each pass. So the blocks of objects written before it is lowered,
    or by a gateway of the same zone configured with a larger value, are
    larger than a reader's.
  - Multisite sync makes blocks of its own size: it compresses what it
    fetches through a 512 KiB `ChunkProcessor`, whatever either zone's
    `rgw_max_chunk_size` (`RGWRadosPutObj::process_attrs`,
    `rgw/driver/rados/rgw_rados.cc:3570-3579`, `:3738-3747`). Its blocks are
    larger than a reader's only where that reader's `rgw_max_chunk_size` is
    below 512 KiB.
  - Each pass hands its 0 bytes on through `send_response_data`'s
    `dump_body` (`rgw/rgw_rest_s3.cc:651-656`, `:768-773`) to the beast
    frontend's `write_data`, an `async_write` of an empty buffer
    (`rgw/rgw_asio_frontend.cc:132-136`). Boost.Asio never touches the
    socket for an empty write and completes it as if posted:
    `reactive_socket_service_base::async_send` marks a send whose buffers
    are all empty a no-op (`boost/asio/detail/reactive_socket_service_base.hpp:315-318`
    in Boost 1.82.0, `:316-319` in 1.87.0), and `do_start_op` hands a no-op
    straight to its immediate completion instead of the reactor
    (`boost/asio/detail/impl/reactive_socket_service_base.ipp:237-256` in
    1.82.0, `:237-258` in 1.87.0). Over TLS, the stream answers an empty
    write with a zero-sized read issued so that the handler runs as if
    posted (`boost/asio/ssl/detail/io.hpp:231-247` in 1.82.0, `:228-244` in
    1.87.0). 1.82.0 and 1.87.0 are the releases Ceph's build fetches when it
    builds its own Boost, at v19.2.6 and v20.2.4
    (`cmake/modules/BuildBoost.cmake:162`, `:166`). So the request's
    coroutine yields to the worker's other connections and resumes, every
    pass.
- **Impact:** once that holds, a GET with a Range that ends early in such a
  block never finishes, even after its client has gone, and keeps a worker
  thread busy, giving it up only between passes; any client that may read
  the object can start one. A GET without a Range is not affected, as its
  range ends at the end of the object. It is config-gated: it needs the
  serving gateway's `rgw_max_chunk_size` below the stored block size, which
  defaults to 4 MiB.
- **Releases:** v19.2.6, v20.2.4 and main 06adccc25d6.
- **rgw-go:** not affected: `compression.Stream` writes the part of each
  decoded block that lies in the range and stops, whatever
  `rgw_max_chunk_size` is (`internal/compression`, `internal/driver`). When
  rgw-go writes compressed objects, blocks larger than a coexisting
  radosgw's `rgw_max_chunk_size` would expose that radosgw to this loop;
  `docs/exclusions.md` records the difference.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. A prior-art search found no report or fix;
  [#74662](https://tracker.ceph.com/issues/74662) and
  [#20098](https://tracker.ceph.com/issues/20098) are distinct, and
  87c7c45ea6a left the loop unchanged. Severity estimate from triage: CVSS
  5.9 (triage estimate). It is unfiled while filing is paused.
- **Found:** phase 1 unit R, Task 4, 2026-10-02, transcribing radosgw's
  decompression for the driver's object read; derived from the source, not
  reproduced.

## radosgw renders GetObjectAttributes's NextPartNumberMarker from an unset variable when max-parts is below 1

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/`; v20.2.4 alone, as v19.2.6 has no
  GetObjectAttributes.
  - `RGWGetObjAttrs_ObjStore_S3::get_params` parses x-amz-max-parts with
    `strict_strtol` and keeps `std::min(*max_parts, 1000)`
    (`rgw/rgw_rest_s3.cc:3952-3962`). `strict_strtol` wraps `strict_strtoll`,
    which is `strtoll` checked only for trailing bytes and range
    (`common/strtol.cc:42-73`, the same at v19.2.6), so 0 and negative values
    pass.
  - `send_response` declares `int next_marker;` with no initializer and
    hands it, with `max_parts ? *max_parts : 1000`, to
    `RadosObject::list_parts` (`rgw/rgw_rest_s3.cc:4076-4097`).
  - With max_parts below 1, `list_parts` sets `*truncated = true` and breaks
    at the first part, before listing any
    (`rgw/driver/rados/rgw_sal_rados.cc:2888-2891`). Its only write of
    `*next_marker` follows a listed part (`:2928`).
  - Because `truncated` is set, `send_response` renders
    NextPartNumberMarker from `next_marker` (`rgw/rgw_rest_s3.cc:4111-4113`),
    whose value is undefined.
  - A part marker that resolves to a part (`rgw_sal_rados.cc:2861-2874`)
    reaches the same break; a marker past the count, or one that names no
    part, returns before it and leaves `truncated` false.
- **Impact:** a GetObjectAttributes request for ObjectParts on a multipart
  object with x-amz-max-parts 0 or negative answers 200 with a
  NextPartNumberMarker whose value is whatever the uninitialized `int`
  holds. Ceph's CMake, spec and debian packaging set no
  `-ftrivial-auto-var-init` (checked at v20.2.4), so an el9 build leaks four
  real bytes of stack; triage leans this a genuine information disclosure
  (partial, pending).
- **Releases:** v20.2.4 and main (06adccc25d6, 2026-10-01,
  `rgw/rgw_rest_s3.cc:4147` and `:4154` for the parse and cap, `:4270` for
  the declaration, `:4304-4305` for the render;
  `rgw/driver/rados/rgw_sal_rados.cc:3068-3069` and `:3108`).
- **rgw-go:** answers the request's part marker, 0 when absent, as
  NextPartNumberMarker; `docs/exclusions.md` records the difference.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response.
- **Found:** phase 1 unit R, Task 6, 2026-10-02, transcribing list_parts'
  truncation for GetObjectAttributes; derived from the source, not
  reproduced.

## radosgw's ObjectParts gives a part without a checksum the checksum of the part before it

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; v20.2.4 alone, as v19.2.6 has
  no GetObjectAttributes.
  - `RadosObject::list_parts` declares `Object::Part obj_part{}` once,
    before its loop (`driver/rados/rgw_sal_rados.cc:2879`). For each part it
    sets the number and size, assigns `obj_part.cksum` only when the part
    head carries a `user.rgw.cksum` that decodes (`:2909-2925`), and passes
    `obj_part` to the caller (`:2927`).
  - `RGWGetObjAttrs_ObjStore_S3::send_response` renders a part's checksum
    whenever its type is not none (`rgw_rest_s3.cc:4091-4094`).
  - So a part whose head has no checksum attr, or one that does not
    decode, is reported with the checksum of the last part before it that
    had one.
  - A part's `user.rgw.cksum` is written only when the upload or the part
    request names a checksum (`RGWPutObj::execute`'s
    `RGWPutObj_Cksum::Factory`, `rgw_op.cc:4601-4607`, and the attr at
    `:4757-4776`), so an upload whose parts mix checksum headers stores
    parts without it.
- **Impact:** not security-relevant. GetObjectAttributes's ObjectParts
  reports a checksum for a part that has none, one belonging to another
  part of the same object.
- **Releases:** v20.2.4 and main (06adccc25d6, 2026-10-01,
  `driver/rados/rgw_sal_rados.cc:3059`, `:3097`, `:3107`;
  `rgw_rest_s3.cc:4284`).
- **rgw-go:** not affected yet: it renders no part checksum.
- **Upstream:** pending: rgw-bug-reproduction classified it on 2026-10-05
  and has not reported a prior-art result.
- **Found:** review of phase 1 unit R, Task 6, 2026-10-04; derived from the
  source, not reproduced.

## radosgw starts an aws-chunked trailer section from a stale leftover count

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `recv_chunk` sets `lf_bytes`, the count of bytes left in `parsing_buf`
    after a chunk's data, only when it copies data out of `parsing_buf`
    (`rgw_auth_s3.cc:1348-1367`, `:1325-1344`; the assignment is `:1358`,
    `:1335`).
  - `complete()` copies `lf_bytes` bytes from `parsing_buf.begin()` into
    the trailer buffer, whatever `parsing_buf.size()` is, and starts the
    trailer read after them (`:1585-1594`, `:1562-1571`).
  - A header that fills `parsing_buf` exactly, 101 bytes with the CRLF
    before it, as a sixteen-digit size field does, leaves it empty once
    consumed (`consumed = semicolon_pos + 83`, `:1183`, `:1160`), so that
    chunk's data is read past `parsing_buf` and `lf_bytes` keeps the count
    an earlier small chunk left.
  - If that chunk is the last data chunk, `complete()` copies that many
    stale bytes, the start of the header just parsed, which the
    `static_vector` still holds, as the start of the trailer section, and
    the trailer read then has that many fewer of its 255 bytes.
- **Impact:** not security-relevant. A signed trailer that would fit radosgw's
  255-byte read can be cut short and fail cleanly with 403
  SignatureDoesNotMatch; the stale bytes are read from inside `parsing_buf`'s
  storage, so there is no out-of-bounds read. It is related to, and distinct
  from, the latent write past an empty `trailer_vec` in "radosgw truncates a
  long aws-chunked trailer section instead of rejecting it".
- **Releases:** checked at v19.2.6 and v20.2.4.
- **rgw-go:** unaffected: its reader counts the trailer section from the
  bytes it reads after the last data chunk, and keeps no leftover count
  (`internal/auth/chunked.go`, `finish`).
- **Upstream:** pending: rgw-bug-reproduction classified it on 2026-10-05
  and has not reported a prior-art result.
- **Found:** phase 1 unit A, Task 6 review, 2026-10-02; derived from the
  source, not reproduced.

## radosgw's signed aws-chunked header parse ignores the key and misframes one not 15 bytes long

- **Kind:** defect, security-relevant: a key shorter than 15 bytes lets an
  authenticated client corrupt the heap or abort radosgw (Impact; triage
  estimate CVSS 6.5-7.7); unfixed through main. Unreproduced: derived from
  the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is
  v19.2.6's, then v20.2.4's, and the code is the same at both and on main.
  - `create_next` looks in a signed header only for a `;`, then an `=`,
    then a CRLF, and takes the 64 bytes between them as the signature;
    it never compares the key with `chunk-signature`
    (`rgw_auth_s3.cc:1140-1169`, `:1117-1146`).
  - It returns `consumed = semicolon_pos + 83`, the length of a header
    whose key is the 15-byte `chunk-signature` (`:1183`, `:1160`), but
    computes the chunk's data offset from the CRLF it found
    (`:1171-1173`, `:1148-1150`). The true length is
    `semicolon_pos + keylen + 68`.
  - So any 15-byte key frames correctly and the chunk verifies. A key of
    another length is misframed: `consumed` is off by the difference, so
    bytes of the header are delivered and hashed as data, or the first
    data bytes are skipped (`get_data_size`, `:1100-1108`, `:1077-1085`),
    until a chunk signature check fails. The trailer section's own parse
    does check its keys (`mut_extract_helper`); only `create_next` omits
    the check.
- **Impact:** a malformed header is accepted with a wrong key, or answered
  with a signature error (403, or 400 XAmzContentSHA256Mismatch for the
  last chunk) rather than refused as malformed. It is gated by the signer:
  `create_next` runs only after the seed signature verifies, the delivered
  bytes equal the hashed bytes, and each chunk signature still gates, so the
  outcome is a rejection or the secret holder corrupting its own object.
  The short key is the exception: `consumed` drives
  `parsing_buf.erase(begin, begin + consumed)` in `recv_chunk`
  (`:1334-1335`, `:1311-1312`) with no bound, and a key shorter than 15
  bytes over-counts it, so on a header with little data after its CRLF
  `consumed` exceeds `parsing_buf.size()`. The erase then runs past the
  end of the 101-byte `static_vector`, a heap corruption or an abort. It
  happens on the first chunk, before any chunk signature is checked, so
  any client whose seed signature verifies can trigger it: an
  authenticated denial of service.
- **Releases:** v19.2.6, v20.2.4 and main; the code is identical.
- **rgw-go:** requires the key `chunk-signature` and refuses any other with
  400 InvalidArgument (`docs/exclusions.md`, "aws-chunked framing is read
  strictly").
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. A prior-art search found no report or fix
  of the unchecked key or the misframe. Related:
  [#45790](https://tracker.ceph.com/issues/45790) (2020, In Progress) calls
  this decode's checking "very limited", and its fix
  [ceph/ceph#35350](https://github.com/ceph/ceph/pull/35350) was closed
  unmerged in 2022; neither names the key check or the misframe, so this is
  not a duplicate. The parse came with def8f6412a5 (2017), and
  [ceph/ceph#54856](https://github.com/ceph/ceph/pull/54856) (2023) reworked
  but did not fix it. The siblings
  [#81122](https://tracker.ceph.com/issues/81122) and
  [#81123](https://tracker.ceph.com/issues/81123) fix adjacent framing
  defects. It is unfiled while filing is paused.
- **Found:** phase 1 unit A, Task 6, 2026-10-02; derived from the source,
  not reproduced.

## radosgw never compares the final aws-chunked chunk's signature and parses its line loosely

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each triple of lines is
  v19.2.6's, then v20.2.4's, then main's (06adccc25d6).
  - `complete()` says it validates "not the final zero-length chunk, but
    the one before that" (`rgw_auth_s3.cc:1557-1559`, `:1534-1536`). It
    computes `final_chunk_signature` under the comment "now it's time to
    verify the signature of the last, zero-length chunk"
    (`:1565-1575`, `:1542-1552`, `:1582-1592`), then only extracts and logs
    the declared `chunk-signature=` value (`:1648-1653`, `:1625-1630`,
    `:1665-1670`) and never compares the two.
  - Its parse of the final chunk line skips an optional "\r\n", "0" and
    ";" independently (`:1624-1637`, `:1601-1614`), so a malformed final
    chunk line, or none, is accepted.
  - Before the decoded length is reached the final chunk is an ordinary
    empty chunk to `recv_chunk`, whose signature is compared
    (`:1284-1291`, `:1261-1268`).
- **Impact:** a conformance and defense-in-depth gap, not a bypass. Every
  data chunk is verified in the HMAC chain, and the final chunk binds no
  new input; where its signature matters, as the seed of the trailer
  signature, radosgw uses its own computed value. The loose parse yields at
  most a 403 for a legitimate client.
- **Releases:** v19.2.6, v20.2.4 and main 06adccc25d6.
- **rgw-go:** reproduces the uncompared signature: once the decoded length
  is delivered the final chunk's declared signature is parsed and not
  compared, and before it the signature is compared, as `recv_chunk` does
  (`internal/auth/chunked.go`, `complete` and `nextChunk`). The final chunk
  line is parsed strictly, and a malformed one is refused with 400
  InvalidArgument (`docs/exclusions.md`, "aws-chunked framing is read
  strictly").
- **Upstream:** [#72253](https://tracker.ceph.com/issues/72253), the Java
  SDK "mcrc32" loose-parse symptom, is Fix Under Review, and
  [#45790](https://tracker.ceph.com/issues/45790) is the loose-parse
  umbrella. [ceph/ceph#64934](https://github.com/ceph/ceph/pull/64934) (for
  #72253) adds exactly the missing comparison and tightens the tail; it was
  closed unmerged by the stale bot after QA failures, its logic not
  rejected. The code came with 5afa3fc52f0
  ([ceph/ceph#54856](https://github.com/ceph/ceph/pull/54856), for
  [#63153](https://tracker.ceph.com/issues/63153)), first in v19.1.0.
- **Found:** before this entry, not by us. Met in phase 1 unit A, Task 6,
  2026-10-02; derived from the source, not reproduced.

## radosgw finds the aws-chunked trailer signature and trailers by substring search anywhere in its window

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; the search is the same at
  v19.2.6, v20.2.4 and main.
  - `complete()` locates `chunk-signature=`, each announced trailer and
    `x-amz-trailer-signature:` by unanchored substring search in the
    trailer window, up to 255 bytes, with `std::string_view::find`
    (`mut_extract_helper` and `extract_helper`, `rgw_auth_s3.cc:1436-1464`
    at v19.2.6, `:1413-1441` at v20.2.4, `:1456-1484` on main 6cafff02b39).
  - When chunks past the decoded length are read into that window, a
    trailer signature validly chained from the chunk at the decoded length
    (`calc_v4_trailer_signature` from `final_chunk_signature`) is accepted
    wherever it sits in the window, a following chunk's data included,
    rather than only on the line after the final chunk.
- **Impact:** a payload whose `x-amz-trailer-signature:`, validly chained
  from the chunk at the decoded length, appears anywhere in the trailer
  window passes. It is gated by that chained HMAC, so an injected
  signature or set of trailers fails closed for anyone without the key, and
  a key holder getting its own trailers accepted from an unusual offset is
  self-inflicted. The same `find`-anywhere search drives the truncation and
  undefined behaviour in "radosgw cuts an aws-chunked trailer section by
  its trailers' length, not their position"; the advance-by-length quirk
  there (`mut_extract_helper` advances by the match's length, not its
  offset plus length, `:1443-1444` at v19.2.6, `:1420-1421` at v20.2.4,
  `:1462-1463` on main) has no effect here, since the extracted
  `chunk-signature=` value is unused.
- **Releases:** v19.2.6, v20.2.4 and main 6cafff02b39.
- **rgw-go:** answers 403 SignatureDoesNotMatch for any payload that
  expects a trailer signature once more data, or the end of the body,
  follows the chunk at the decoded length, so it never reads a signature
  from the window (`internal/auth/chunked.go`, `endAtLength`), and its own
  trailer parse reads the section line by line (`docs/exclusions.md`,
  "aws-chunked trailer sections are read line by line").
- **Upstream:** none. A prior-art search found no report or fix; it is
  distinct from [#81122](https://tracker.ceph.com/issues/81122) and
  [#81123](https://tracker.ceph.com/issues/81123), the same function's
  other framing defects, and from
  [#45790](https://tracker.ceph.com/issues/45790), the robustness umbrella
  that does not name it. The trailer parse came with
  [ceph/ceph#54856](https://github.com/ceph/ceph/pull/54856). It is unfiled
  while filing is paused, and a report waits on a cluster reproduction.
- **Found:** phase 1 unit A, Task 6 re-review, 2026-10-04; derived from the
  source, not reproduced.

## Squid's cls_rgw reshard guard and Squid's radosgw reshard wait disagree, so guard_reshard spins without waiting

- **Kind:** defect on Squid. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`.
  - Squid's object class refuses a guarded index write whenever the shard
    header's reshard status is not NOT_RESHARDING (`guard_bucket_resharding`
    -> `header.resharding()`, `cls/rgw/cls_rgw.cc:4597`; `resharding()` is
    `reshard_status != NOT_RESHARDING`, `cls/rgw/cls_rgw_types.h:779-781`).
  - But Squid's `block_while_resharding` treats only IN_PROGRESS as busy
    (`resharding_in_progress()`, `driver/rados/rgw_rados.cc:7837`;
    `cls_rgw_types.h:783-785`), refreshes the bucket and returns 0
    otherwise, and both `guard_reshard` callers then reset their retry
    counter and re-send at once (`UpdateIndex::guard_reshard`,
    `rgw_rados.cc:7062`; the completion retry thread's
    `RGWRados::guard_reshard`, `:7760`).
  - So while a shard's status is DONE, a Squid radosgw write, or its retry
    thread, loops with no wait and no bound, each pass a refused write, a
    status read and a bucket-info read.
  - At v20.2.4 a Tentacle radosgw reads any non-zero status as busy
    (`resharding()`, `:8779`) and waits, and the Tentacle class refuses only
    IN_PROGRESS and an IN_LOGRECORD shard at `rgw_reshardlog_threshold`
    (`cls_rgw.cc:906-921`).
- **Impact:** a Squid radosgw spins a worker on an index write, or its
  completion retry thread, while a shard is left at a reshard status its
  class rejects but its wait does not treat as busy. By code reading it is
  reachable with a Squid radosgw against Tentacle OSDs during an upgrade,
  while a Tentacle reshard holds a shard IN_LOGRECORD past the threshold, or
  with a Squid-class shard left at DONE. Not reproduced.
- **Releases:** v19.2.6 (Squid); the Tentacle wait and class guard at
  v20.2.4.
- **rgw-go:** bounds it. `guardReshard` waits with `blockWhileResharding`
  and re-sends at most ten times, and starts that count over only when the
  refreshed instance moves the object to another shard object
  (`internal/driver/indexop.go`); a shard that keeps refusing while its
  status reads as finished uses up the ten calls and answers 500
  UnknownError. `docs/exclusions.md` records the difference.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit W, Task 4, 2026-10-04, implementing the index
  reshard guard; derived from the source, not reproduced.

## radosgw's garbage collector runs one more concurrent IO than rgw_gc_max_concurrent_io

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** `RGWGCIOManager::schedule_io` waits only while
  `ios.size() > max_aio`, where `max_aio` is `rgw_gc_max_concurrent_io`
  (`src/rgw/driver/rados/rgw_gc.cc:371` and `:384` at v19.2.6, `:385` and
  `:399` at v20.2.4, `:307` and `:321` on main 06adccc25d6). So up to
  `max_aio + 1` operations are in flight, and a value of 0 still allows one.
  The option is a plain int with default 10 and no minimum
  (`src/common/options/rgw.yaml.in`).
- **Impact:** garbage collection keeps one more RADOS operation in flight
  than the option names; minor.
- **Releases:** v19.2.6, v20.2.4 and main.
- **rgw-go:** keeps `rgw_gc_max_concurrent_io` as radosgw reads it
  (`internal/driver`, `writer.go`), and its gc worker keeps up to one more
  put than the option in flight, as `schedule_io` does
  (`internal/driver/gc.go`), which a spec pins.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit W, Task 4, 2026-10-04; derived from the source,
  not reproduced.

## radosgw re-reads a resharding bucket by name, so a write can land in a bucket recreated under that name

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `block_while_resharding`'s `fetch_new_bucket_info` re-reads the bucket
    by tenant and name (`get_bucket_info`,
    `driver/rados/rgw_rados.cc:7793`, `:8735`; `:9400` on main 06adccc25d6)
    into the `UpdateIndex` target's bucket info, and `bs->init` takes the
    shard from it (`:7802`, `:8744`).
  - If the bucket was removed and a bucket of the same name created while
    the write waited out the reshard, the prepare and completion go to the
    new bucket's index, although the head object was written under the old
    bucket's marker.
- **Impact:** a write that waits out a reshard can index into a different
  bucket that reused the name.
- **Releases:** v19.2.6, v20.2.4 and main.
- **rgw-go:** re-reads the bucket instance by its id, not its name
  (`refreshIndexOp`, `internal/driver/indexop.go`), so a bucket gone when
  the write resumes is 404 NoSuchKey and a same-name bucket created in that
  time does not receive the write's entries. `docs/exclusions.md` records
  the difference.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit W, Task 4, 2026-10-04; derived from the source,
  not reproduced.

## radosgw turns a negative rgw_gc_max_concurrent_io or rgw_gc_max_trim_chunk into a huge unsigned limit

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/driver/rados/`; both options are
  plain ints with no minimum (`src/common/options/rgw.yaml.in`).
  - `RGWGCIOManager` stores `rgw_gc_max_concurrent_io` in a `size_t
    max_aio` (`rgw_gc.cc:365` and `:371` at v19.2.6, `:379` and `:385` at
    v20.2.4), so a negative value becomes a count near 2^64 and
    `schedule_io` never waits (`ios.size() > max_aio` is never true,
    `:384`, `:399`).
  - The trim flush compares `rt.size()` with
    `(size_t)rgw_gc_max_trim_chunk` (`:462`, `:477`), so a negative chunk
    never flushes until garbage collection drains.
- **Impact:** not security-relevant: only an administrator sets the
  options. A negative `rgw_gc_max_concurrent_io` lets garbage collection
  schedule RADOS operations without bound; a negative
  `rgw_gc_max_trim_chunk` defers every tag trim to the drain.
- **Releases:** checked at v19.2.6 and v20.2.4.
- **rgw-go:** keeps both options as radosgw reads them (`internal/driver`,
  `writer.go`), and its gc worker converts them as `RGWGCIOManager` does
  (`internal/driver/gc.go`), so a negative one bounds nothing there either.
- **Upstream:** pending: rgw-bug-reproduction classified it on 2026-10-05
  and has not reported a prior-art result.
- **Found:** phase 1 unit W, Task 4 review, 2026-10-04; derived from the
  source, not reproduced.

## Squid's is_public counts every Allow statement whose NotPrincipal is not the wildcard

- **Kind:** defect, fixed in Tentacle and unfixed on squid. Unreproduced:
  derived from the source.
- **Evidence:** paths are under `src/rgw/`.
  - At v19.2.6, `IsPublicStatement` returns the wildcard statement's
    condition result when a Principal entry is the wildcard, and otherwise,
    for every other Allow statement, `std::none_of` over its NotPrincipal
    entries for the wildcard (`rgw_iam_policy.cc:1900-1916`). `none_of` over
    an empty set is true, so an Allow statement that names a specific
    Principal, or names none, counts as public whatever its conditions.
  - v20.2.4 counts an Allow statement only when a Principal entry is the
    wildcard and its conditions hold in `iam_all_env`
    (`rgw_iam_policy.cc:1935-1947`). The change is 019aaa4d101 ("rgw: donot
    check for NotPrincipal in IsPublicStatement", 2024-07-19), whose message
    names the empty-NotPrincipal case.
  - `is_public` decides PutBucketPolicy's BlockPublicPolicy refusal
    (`rgw_op.cc:8103-8108` at v19.2.6) and GetBucketPolicyStatus's IsPublic
    (`rgw_op.cc:8597`).
- **Impact:** on Squid, a bucket whose public-access block sets
  BlockPublicPolicy refuses, with 403, any bucket policy holding an Allow
  statement for a named principal, such as a grant to one user;
  GetBucketPolicyStatus reports such a policy public. It over-blocks and
  fails closed.
- **Releases:** v19.2.6 and the squid branch head (a742f50616e, 2026-09-03),
  which still has the `none_of`. The first tag carrying the fix is v20.0.0,
  released v20.2.0; checked at v20.2.4. Older squid releases not checked.
- **rgw-go:** follows the zone's release: `policy.Semantics`'s
  `PublicNeedsWildcardPrincipal` selects v19.2.6's rule on Squid and
  v20.2.4's on Tentacle (`internal/policy/policy.go`, `Policy.IsPublic`).
- **Upstream:** [#67047](https://tracker.ceph.com/issues/67047), with its
  duplicate [#67048](https://tracker.ceph.com/issues/67048), reported it;
  the fix 019aaa4d101 came with
  [ceph/ceph#58686](https://github.com/ceph/ceph/pull/58686), first tagged
  v20.0.0 and released v20.2.0. The squid backport tracker
  [#67176](https://tracker.ceph.com/issues/67176) is open with no PR, and
  the reef one [#67177](https://tracker.ceph.com/issues/67177) was rejected.
  Not found by us; it is an over-block, not a vulnerability, so there is
  nothing to file. The opposite direction, Squid's false negative for a
  wildcard-principal statement behind a condition on a key outside
  `iam_all_env`, is "radosgw's is_public judges a wildcard-principal
  statement against a fixed three-key environment".
- **Found:** phase 1 unit Z, Task 4, 2026-10-04, transcribing `is_public`
  at both tags; derived from the source, not reproduced.

## radosgw ignores NotResource in a statement that also names Resource

- **Kind:** defect, potentially security-relevant, under a condition: an
  Allow that writes both keys grants the resources it meant to except.
  Unfixed through main. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `Statement::eval` matches Resource when it is non-empty and consults
    NotResource only in the `else if` (`rgw_iam_policy.cc:1206-1220`,
    `:1210-1224`).
  - The parser accepts both keys in one statement: its duplicate-key check
    covers only a repeated key (`rgw_iam_policy.cc:277-329`, `:287-339`;
    the `!pp->test(k->id)` at `:504`, `:514`), and it files each ARN under
    the key that named it (`:680-702`, `:693-715`).
  - main (06adccc25d6, 2026-10-01) keeps the `else if`
    (`rgw_iam_policy.cc:1320`).
- **Impact:** an Allow statement with Resource `arn:aws:s3:::b/*` and
  NotResource `arn:aws:s3:::b/secret` grants `b/secret`. A Deny so written
  denies the excepted objects as well. AWS refuses a statement with both
  keys; radosgw stores it and drops the exception.
- **Releases:** v19.2.6, v20.2.4 and main.
- **rgw-go:** reproduces it: `policy.Parse` accepts a statement with both
  keys, as radosgw's parser does, and `policy.Statement.Eval`
  (`internal/policy/statement.go`) consults NotResource only when Resource
  is empty.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. A prior-art search found no report
  ([#58929](https://tracker.ceph.com/issues/58929) is adjacent,
  [#68029](https://tracker.ceph.com/issues/68029) unrelated) and no fix. The
  `else if` is original to 24d295237ef ("rgw: policy: fix NotPricipal,
  NotResource does not take effect", 2018, first tagged v14.0.1), which
  created it while fixing a different bug; cite it as the origin, not prior
  art. It is unfiled while filing is paused.
- **Found:** phase 1 unit Z, Task 4, 2026-10-04, transcribing
  `Statement::eval`; derived from the source, not reproduced.

## radosgw's princ_type after Policy::eval reflects the last statement evaluated, not the one that matched

- **Kind:** defect on the squid and tentacle branches; main lost it to a
  refactor. Leaning security-relevant, not yet settled: it can flip an STS
  session-policy decision to grant when a later statement names the
  assumed-role session principal (Impact). Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `Statement::eval_principal` resets `*princ_type` to Other on entry, then
    for a role a non-empty Principal names sets it to Session or Role from
    that statement's Principal block (`rgw_iam_policy.cc:1246-1273`,
    `:1250-1277`).
  - `Policy::eval` passes one `princ_type` to every statement in turn
    (`:1828-1842`, `:1863-1877`), so after it returns the value reflects the
    last statement evaluated, not the one that allowed. It depends only on
    the identity and the Principal blocks, not on the requested action or
    resource, so it is deterministic and not caller-steerable.
  - The consumer, `evaluate_iam_policies`, reads `princ_type` only when
    session policies are present (`rgw_common.cc:1185-1228`, `:1198-1239`).
  - On main f7c44ac833e rewrote `Policy::eval` to return the matched
    principal and set it only for a matching Allow (`:2080-2111` at
    06adccc25d6); the squid (a742f50616e) and tentacle (7411a080411) branch
    heads keep the reset-per-statement shape.
- **Impact:** with STS session policies, the branch taken can be wrong for
  the matched statement. Usually it is an unexpected 403; narrowly it is a
  fail-open, where a resource policy that allows the action to the role ARN
  and a later statement names the session or user ARN lands on the Session
  branch and grants without the session policy. It leaks only what the
  resource policy already grants that role or session, with no cross-account
  or outsider gain. Low.
- **Releases:** v19.2.6, v20.2.4 and both branch heads above; not main.
- **rgw-go:** does not compute the principal type
  (`policy.Statement.EvalPrincipal`, `internal/policy/statement.go`); phase
  1 has no STS, so the type is unread until then.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. A prior-art search found no report
  ([#73796](https://tracker.ceph.com/issues/73796) and
  [#68029](https://tracker.ceph.com/issues/68029) are unrelated) and no
  targeted fix; f7c44ac833e, which incidentally fixed it on main, came with
  [ceph/ceph#66999](https://github.com/ceph/ceph/pull/66999) for
  [#74471](https://tracker.ceph.com/issues/74471), a different defect. It is
  not backported: the tentacle backport
  [ceph/ceph#70533](https://github.com/ceph/ceph/pull/70533) carries only
  the logging of [ceph/ceph#67732](https://github.com/ceph/ceph/pull/67732),
  so both floors keep the defect. It is unfiled while filing is paused.
- **Found:** phase 1 unit Z, Task 4 review, 2026-10-04, transcribing
  `eval_principal` and `Policy::eval`; derived from the source, not
  reproduced.

## radosgw accepts a policy statement with no Effect and evaluates it as Deny

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - A `Statement` initializes its effect to `Effect::Deny`, under the
    comment "Every statement MUST provide an effect. I just initialize it to
    deny as defensive programming" (`rgw_iam_policy.h:539-541`, `:592-594`).
  - `ParseState::obj_start` creates each statement with `emplace_back`
    (`rgw_iam_policy.cc:784-788`, `:803-807`), `ParseState::key` sets the
    effect only in the Effect branch (`:617-624`, `:630-637`), and
    `ParseState::obj_end` has only a duplicate-key guard, no required-key
    check (`:448-462`, `:458-472`). So a statement that names no Effect
    parses and keeps Deny.
- **Impact:** non-security and fail-safe. A policy author who omits Effect
  silently loses the grant they meant, with no unintended access. AWS
  rejects a statement with no Effect (not checked against AWS documentation
  here).
- **Releases:** v19.2.6, v20.2.4 and main (06adccc25d6, 2026-10-01,
  `rgw_iam_policy.h:703`).
- **rgw-go:** reproduces it: `policy.Parse` accepts a statement with no
  Effect and leaves it at Deny, `Effect`'s zero value
  (`internal/policy/statement.go`), as radosgw's default does.
- **Upstream:** none. A prior-art search found no report
  ([#73983](https://tracker.ceph.com/issues/73983) and
  [#68029](https://tracker.ceph.com/issues/68029) are adjacent or unrelated)
  and no fix. It is unfiled while filing is paused, and a report waits on a
  cluster reproduction.
- **Found:** phase 1 unit Z, Task 4 review, 2026-10-04, transcribing the
  statement's default effect; derived from the source, not reproduced.

## Tentacle's radosgw aborts on a policy whose Statement is a string

- **Kind:** defect, security-relevant: a regression from v19.2.6, unfixed
  through main. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`.
  - `ParseState::do_string` takes the current statement as `t`, null until
    the first statement object opens, and at v20.2.4 asserts
    `ceph_assert(t || w->id == TokenID::Version || w->id == TokenID::Id)`
    (`rgw_iam_policy.cc:608-609`; main `:754-755` at 6cafff02b39,
    2026-10-04). `ceph_assert` is compiled into release builds and aborts
    the process.
  - A string under the top-level Statement key reaches it with `w` the
    Statement keyword and no statement yet: `{"Statement": "x"}` or
    `{"Statement": ["x"]}`. `key` pushes the Statement state for the key
    (`rgw_iam_policy.cc:504-517`), and only `obj_start` appends a statement
    (`:803-811`).
  - v19.2.6 has no assertion: the same string falls to the final `else`,
    which refuses it with "`x` is not valid in the context of `Statement`."
    without touching `t` (`rgw_iam_policy.cc:596`, `:742-746`).
  - The assertion came with 04f26b29e0c, "Checking for dereference of a null
    pointer (loaded from variable 't')" (2024-09-11), whose message names
    tracker #68029.
  - Every caller of `Policy`'s constructor reaches it: PutBucketPolicy
    parses the request body (`rgw_op.cc:9024-9027`), and so do the IAM user,
    group and role policy APIs (`rgw_rest_user_policy.cc:167`,
    `rgw_rest_iam_group.cc:1284`, `rgw_rest_role.cc:189` and `:593`), SNS
    topic policies (`rgw_rest_pubsub.cc:155`) and STS session policies
    (`rgw_rest_sts.cc:966` and `:1027`).
- **Impact:** an authenticated remote denial of service. A user allowed
  PutBucketPolicy on any bucket, as every bucket owner is, ends the radosgw
  process with the 18-byte body `{"Statement": "x"}`; `["x"]` and
  `["x", {...}]` do too, `[{...}, "x"]` does not. The abort is not an
  exception, so the call sites' `catch (PolicyParseException&)` cannot
  stop it. The parse comes after the permission check, so the other entry
  points need their own permissions. PutBucketPolicy parses before it
  stores, so the crash is repeatable but paced by the attacker, not
  durable. Open: whether a path that stores policy text without parsing it
  (multisite metadata sync, `radosgw-admin metadata put`, a restore) lets a
  later parse of the stored text make it a crash loop; that needs a
  cluster.
- **Releases:** v20.2.4 and main; v19.2.6 refuses the document instead. The
  assertion was never backported to squid or reef.
- **rgw-go:** differs on Tentacle: `policy.Parse` refuses the document with
  v19.2.6's annotation on both releases (`docs/exclusions.md`).
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. rgw-bug-reproduction's prior-art search
  found no tracker issue and no fix. The cause is 04f26b29e0c, which came
  with [ceph/ceph#59731](https://github.com/ceph/ceph/pull/59731) for
  [#68029](https://tracker.ceph.com/issues/68029): its fix for a clang-tidy
  null dereference added the assertion. It is unfiled while filing is
  paused.
- **Found:** phase 1 unit Z, Task 5, 2026-10-04, transcribing `do_string`;
  derived from the source, not reproduced.

## radosgw aborts on a stored IAM policy attr with bytes past its encoding

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's, and a single line is the same at both.
  - `load_inline_policy` and `load_managed_policy` decode a user's or
    group's inline policies, a `std::map<string, string>`, and its managed
    policies, `ManagedPolicies`, with `decode(x, bl)` on the attr's whole
    bufferlist (`rgw/rgw_auth.cc:91` and `:102`).
  - That overload is the full-bufferlist decoder, which asserts that the
    decode consumed every byte: `ceph_assert(p.end())`
    (`include/encoding.h:632-637`, `:633-638`; main `:1486-1491` at
    6cafff02b39). `ceph_assert` aborts in release builds. A decode that runs
    short throws `buffer::error`, which authentication turns into -EPERM
    (`rgw/rgw_auth.cc:548-554`, `:563-569`); bytes left over are not an
    exception.
  - `load_account_and_policies` loads a user's attrs this way for every
    request it authenticates (`rgw/rgw_auth.cc:163-168`), and each of the
    user's groups' attrs (`:126-131`).
- **Impact:** not security-relevant. A user or group whose
  `user.rgw.user-policy`, `user.rgw.managed-policy` or group policy attr
  carries a trailing byte ends the radosgw process at that user's next
  request, and at each request after a restart. A client cannot write such
  an attr: PutUserPolicy re-encodes the map it stores. radosgw writes these
  attrs cleanly, so the trigger is corruption, an administrator's
  `radosgw-admin metadata put`, or another writer sharing the pools; a
  multisite metadata sync between versions that encode the attr
  differently is a possible case.
- **Releases:** v19.2.6, v20.2.4 and main.
- **rgw-go:** differs: `policy.DecodeUserPolicies` and
  `policy.DecodeManagedPolicies` fail on bytes past the encoding, so the
  request is refused as for an attr that does not decode
  (`docs/exclusions.md`).
- **Upstream:** pending: rgw-bug-reproduction classified it on 2026-10-05
  and has not reported a prior-art result.
- **Found:** phase 1 unit Z, Task 5, 2026-10-04, transcribing
  `load_inline_policy`; derived from the source, not reproduced.

## radosgw's ARN ordering is not a strict weak ordering

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's, and a single line is the same at both.
  - `operator<(const ARN&, const ARN&)` is the disjunction of the fields'
    own `<`: partition, service, region, account or resource
    (`rgw_arn.cc:309-315`; main `:311-317` at 6cafff02b39). Two ARNs that
    differ in two fields in opposite directions each compare less than the
    other, as `arn:aws:sns:zg:tenant-b:topic-a` and
    `arn:aws:sns:zg:tenant-a:topic-b` do, so the ordering is not the strict
    weak ordering the standard containers require. Neither is less than the
    other only when every field is equal.
  - A statement keeps its Resource and NotResource ARNs in
    `boost::container::flat_set<ARN>` (`rgw_iam_policy.h:546-547`,
    `:599-600`). Its binary search may miss an equal ARN, so a repeated ARN
    can be kept twice; a distinct one is never dropped.
  - CreateBucketNotification keeps the topics it loads in
    `std::map<rgw::ARN, rgw_pubsub_topic>` (`rgw_rest_pubsub.cc:1135`,
    `:1146`), filled by `emplace` in `init_processing` (`:1242-1253`,
    `:1254-1265`) and read back by `find` for each notification, which skips
    the notification when the topic is not found (`:1315-1318` and
    `:1383-1386`, `:1327-1330` and `:1395-1398`). With the two ARNs above,
    the second is inserted to the left of the first, and `find` for it goes
    right from the first and ends at `end()`.
- **Impact:** not security-relevant. Policy evaluation asks only whether
  some resource matches, so a repeated ARN changes nothing there. A bucket
  notification configuration naming two topics whose ARNs order
  inconsistently, as topics of two tenants or accounts can, loses a
  notification while the request succeeds; the bucket owner writes that
  configuration.
- **Releases:** v19.2.6, v20.2.4 and main.
- **rgw-go:** keeps a statement's resources in document order, each once
  (`policy.Parse`); evaluation is unaffected. Its bucket notifications are
  not written yet.
- **Upstream:** pending: rgw-bug-reproduction classified it on 2026-10-05
  and has not reported a prior-art result.
- **Found:** phase 1 unit Z, Task 5, 2026-10-04, choosing the order of
  parsed resources; derived from the source, not reproduced.

## radosgw's policy parser forgets a statement's keys when an object in an array closes

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's, and a single line is the same at both.
  - The parser refuses a key a statement gives twice through its `seen`
    bits; `set` also records a statement's keys and principal types in `v`
    (`rgw_iam_policy.cc:318-329`, `:328-339`), and `reset` clears them from
    `seen` (`:370-373`, `:380-383`).
  - `ParseState::obj_end` calls `reset` whenever an object closes inside an
    array (`rgw_iam_policy.cc:448-462`, `:458-472`; main `:669-683` at
    6cafff02b39), which is meant for the Statement array. Condition,
    NotPrincipal and every condition operator are also arrayable and
    objectable (`rgw_iam_policy_keywords.gperf:28`, `:33` and `:38-77`).
  - So after `"Condition": [{...}]`, `"NotPrincipal": [{...}]` or
    `"StringEquals": [{...}]`, the statement may give again any key it gave
    before. A second Effect or Sid replaces the first
    (`rgw_iam_policy.cc:615-619`, `:628-632`); a second Action, Resource or
    Principal adds to it. The same reset lets Principal and NotPrincipal both
    name the AWS type, which `dex`'s shared bits otherwise refuse.
- **Impact:** not security-relevant. A statement such as
  `"Effect": "Deny", "Condition": [{...}], "Effect": "Allow"` parses and
  allows: the second value wins. The policy's author writes it, so nothing
  is escalated, but a reader of the policy sees the Deny, and the parser's
  own duplicate-key check, which refuses the same keys without the array,
  does not hold.
- **Releases:** v19.2.6, v20.2.4 and main.
- **rgw-go:** reproduces it (`policy.Parse`), since a stored policy must
  parse as the zone's radosgw parses it.
- **Upstream:** pending: rgw-bug-reproduction classified it on 2026-10-05
  and has not reported a prior-art result.
- **Found:** phase 1 unit Z, Task 5, 2026-10-04, transcribing `obj_end`;
  derived from the source, not reproduced.

## radosgw refuses a JSON true, false or null in a policy with "No error?"

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - rapidjson's `BaseReaderHandler` hands `Null` and `Bool` to the
    handler's `Default` (`s3select/rapidjson/include/rapidjson/reader.h:203-205`
    at rapidjson fcb23c2d, the submodule both tags build).
    `PolicyParser::Default` returns false without setting an annotation
    (`rgw/rgw_iam_policy.cc:436-438`, `:446-448`; main `:657-659` at
    6cafff02b39), so the parse fails with kParseErrorTermination.
  - `PolicyParseException` then reports the parser's annotation, which still
    holds its initial value, "No error?" (`rgw/rgw_iam_policy.cc:275`,
    `:285`; main `:496`), as "At character offset N, No error?"
    (`rgw/rgw_iam_policy.h:563-579`, `:616-632`). PutBucketPolicy returns
    that message to the client (`rgw/rgw_op.cc:8116-8120`, `:9047-9051`).
  - So every unquoted `true`, `false` or `null`, wherever it appears, fails
    the parse. AWS's policy grammar makes the quotation marks around
    Boolean and numeric values optional, so
    `"Bool": {"aws:SecureTransport": false}` is a valid AWS policy.
- **Impact:** not security-relevant: it fails closed, and every path that
  stores a policy constructs it first, so the refusal stores nothing.
  radosgw refuses a valid AWS policy that writes a Boolean condition value
  without quotation marks, and tells the client there was "No error?". The
  quoted form, `"false"`, parses.
- **Releases:** v19.2.6, v20.2.4 and main.
- **rgw-go:** reproduces it: `policy.Parse` refuses a literal with the same
  message and offset (`internal/policy/parse.go`), since a policy rgw-go
  stores must parse in the zone's radosgw.
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no report or
  fix; [#74736](https://tracker.ceph.com/issues/74736) and
  [#64189](https://tracker.ceph.com/issues/64189) are distinct. The fix is to
  handle `Bool()` and `Null()` as `String()` and `RawNumber()` are; a
  low-priority usability report once filing resumes.
- **Found:** phase 1 unit Z, Task 5 review, 2026-10-04, transcribing
  `PolicyParser::Default`; derived from the source, not reproduced.

## Squid's PUT refuses a quoted If-Match that matches the object's ETag

- **Kind:** defect, fixed in Tentacle and, after v19.2.6, in Squid; the
  prefix acceptance below is unfixed at v20.2.4. The quoted rejection fails
  closed and is not security-relevant; the prefix acceptance fails open,
  but its reach is low, since the condition must start with the current
  ETag. Unreproduced here: derived from the source.
- **Evidence:** paths are under `src/rgw/`.
  - PutObject reads If-Match and If-None-Match raw
    (`RGWPutObj_ObjStore_S3::get_params`, `rgw_rest_s3.cc:2622-2623` at
    v19.2.6, `:2783-2784` at v20.2.4) and hands them to the head write
    (`rgw_op.cc:4559-4560` at v19.2.6, `:4838` at v20.2.4).
  - Squid compares If-Match with `strncmp(if_match, etag, etag.length())`
    against the stored ETag, which is the bare hex digest, stored without a
    NUL (`rgw_op.cc:4522` at v19.2.6; `prepare_atomic_modification`,
    `driver/rados/rgw_rados.cc:6515-6527` at v19.2.6, the comparison at
    `:6524`). An If-Match in the quoted form RFC 7232 gives an entity tag,
    the form an ETag response header carries and AWS SDKs send back, starts
    with `"`, so it never matches: the PUT fails 412 PreconditionFailed
    though the object's ETag is the one named. A bare digest matches. The
    same raw comparison applies to a quoted If-None-Match, which then never
    refuses (`:6536-6542`).
  - The comparison stops at the ETag's length, so any If-Match that begins
    with the current ETag matches, whatever follows it: `"<etag>"` fails only
    for its leading quote, and `<etag>x` passes. Tentacle's
    `check_preconditions` unquotes both conditions with `rgw_string_unquote`
    first (`driver/rados/rgw_rados.cc:7296` and `:7318` at v20.2.4) but keeps
    the prefix comparison (`:7299` and `:7321`), so a condition that begins
    with the ETag still matches there.
  - An If-Match on a missing object, `*` or an ETag, answers 412 on Squid
    (`:6519`, `:6523-6525` at v19.2.6) and ENOENT, 404, on Tentacle
    (`:7291`, `:7304` at v20.2.4).
- **Impact:** on Squid a conditional PUT with a quoted If-Match fails 412
  whatever the object holds, and one with a quoted If-None-Match naming the
  current ETag overwrites the object; clients that send ETags as they
  received them cannot use either condition. On both releases a condition
  that only begins with the current ETag counts as naming it.
- **Releases:** v19.2.6 for the quoted condition; v19.2.6 and v20.2.4 for
  the prefix.
- **rgw-go:** checks the conditions as v20.2.4 does on both releases:
  quoted conditions are unquoted, and the prefix comparison is kept
  (`checkPreconditions`, `internal/driver/headwrite.go`; `docs/exclusions.md`,
  "Write conditions are Tentacle's on Squid too").
- **Upstream:** [#64439](https://tracker.ceph.com/issues/64439) (open; a
  quoted If-Match is rejected) and
  [#80924](https://tracker.ceph.com/issues/80924) (open; the prefix
  comparison accepts any If-Match that begins with the current ETag, which
  survives the fix at v20.2.4 `rgw_rados.cc:7299`). The quoted condition is
  fixed by [ceph/ceph#63348](https://github.com/ceph/ceph/pull/63348), with
  backports [ceph/ceph#65949](https://github.com/ceph/ceph/pull/65949)
  (tentacle, in v20.2.4) and
  [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932) (squid, merged
  after v19.2.6).
- **Found:** phase 1 unit W, Task 5, 2026-10-04, writing the head write's
  preconditions; derived from the source, not reproduced. Upstream reported
  it first.

## Squid's write conditions fail an If-None-Match ETag on a missing key and skip a head without a write tag

- **Kind:** defect, and a security issue: an integrity bypass that defeats
  If-None-Match: * and If-Match and so loses an update (triage estimate
  CVSS about 6.5). Fixed in Tentacle and, after v19.2.6, in Squid.
  Unreproduced here: derived from the source.
- **Evidence:** paths are under `src/rgw/driver/rados/`; lines are
  `rgw_rados.cc` at v19.2.6 unless marked.
  - `prepare_atomic_modification` checks a write's conditions only inside
    `if (need_guard)`, and `need_guard` is false whenever the head's tag is
    fake (`:6493-6495`, `:6508-6544`). `get_obj_state_impl` fakes the tag of
    a head that has a manifest and no `user.rgw.idtag` (`:6238-6245`), as a
    head written before radosgw kept write tags has. On such a head no
    condition is checked: If-Match with any ETag passes, and so does
    If-None-Match: *, after which the reset overwrites the head
    (`:6546-6553`).
  - With an If-None-Match naming an ETag, a key without an object, or an
    object stored without an ETag, has no etag attr, and
    `!state->get_attr(RGW_ATTR_ETAG, bl)` refuses the PUT with 412
    PreconditionFailed (`:6536-6542`), though no ETag matches.
  - Tentacle checks the conditions first, whatever the guard
    (`check_preconditions`, called at `:3294-3297` at v20.2.4), and passes an
    If-None-Match ETag when the object has no ETag (`:7315-7324` at
    v20.2.4).
- **Impact:** on Squid, If-None-Match: * does not stop an overwrite of a
  head without a write tag, and If-Match does not stop one whose ETag has
  changed, so a conditional writer overwrites an update it meant to keep;
  and a PUT with an If-None-Match ETag fails 412 on a new key, or on an
  object stored without an ETag.
- **Releases:** v19.2.6. v20.2.4 is not affected.
- **rgw-go:** checks the conditions as v20.2.4 does on both releases
  (`guardHead` and `checkPreconditions`, `internal/driver/headwrite.go`;
  `docs/exclusions.md`, "Write conditions are Tentacle's on Squid too").
- **Upstream:** [#68183](https://tracker.ceph.com/issues/68183) (Resolved);
  related: [#80925](https://tracker.ceph.com/issues/80925),
  [#80906](https://tracker.ceph.com/issues/80906),
  [#80922](https://tracker.ceph.com/issues/80922),
  [#80907](https://tracker.ceph.com/issues/80907) and
  [#80898](https://tracker.ceph.com/issues/80898). Fixed by
  [ceph/ceph#63348](https://github.com/ceph/ceph/pull/63348), with
  backports [ceph/ceph#65949](https://github.com/ceph/ceph/pull/65949)
  (tentacle, in v20.2.4) and
  [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932) (squid, merged
  after v19.2.6). The same pull request added conditional DELETE and
  brought in the regression in "Tentacle's DeleteObject removes the head
  without checking its write tag".
- **Found:** phase 1 unit W, Task 5, 2026-10-04, writing the head write's
  preconditions; derived from the source, not reproduced. Upstream reported
  it first.

## radosgw's send_split_chain repeats an object in its remainder and loops on an object too large for an entry

- **Kind:** defect, not security-relevant, unfixed through main.
  Unreproduced: derived from the source.
- **Evidence:** `RGWGC::send_split_chain` is the same at v19.2.6 and
  v20.2.4 (`driver/rados/rgw_gc.cc:68-118`) and on main (`:69-119` at
  6cafff02b39).
  - It adds each object of an overwritten or deleted object's tail chain
    to a batch, and when the batch's estimated encoding passes
    `rgw_max_chunk_size`, it takes the object out again, steps its iterator
    back (`--it`, `:89-90`) and sends the batch (`:92`). On success the loop's
    `it++` returns to the object, which starts the next batch.
  - When the send fails, the remainder to delete inline starts at the
    stepped-back iterator (`:94`), the batch's last object, which the batch
    already holds: `delete_objs_inline` puts that object's ref twice. The
    second put is a no-op, the tag being retired or the object gone.
  - An object whose estimate alone passes `rgw_max_chunk_size` leaves the
    batch empty after it is taken out. The first object of the chain steps
    the iterator back from the list's start, which is undefined; any later
    one makes the loop send an empty entry and come back to the same
    object, without end while the shard accepts the entries.
    `rgw_max_chunk_size` has no minimum (`common/options/rgw.yaml.in:84-98`
    at v19.2.6), and one tail's estimate is a few hundred bytes.
- **Impact:** not a security issue. The repeated object is harmless:
  `delete_objs_inline` puts by refcount tag, so the second put with the
  same tag only repeats a request and logs ENOENT or changes nothing. The
  loop has no bound, but no client reaches it: an object's estimate comes
  from its oid, a few hundred bytes and at most a few KB, against
  `rgw_max_chunk_size`'s 4 MiB default, so only an operator who lowers the
  option to that scale makes an overwrite or delete of an object with
  tails never finish. Open: a path that gives one entry an estimate near
  4 MiB, or a deployment that lowers `rgw_max_chunk_size` near an entry's
  size, would make the loop an authenticated denial of service.
- **Releases:** checked at v19.2.6, v20.2.4 and main.
- **rgw-go:** reproduces the repeated object, and queues an object too
  large for an entry in an entry of its own (`enqueueGC`,
  `internal/driver/gc_enqueue.go`; `docs/exclusions.md`, "A GC chain object
  too large for one entry is queued alone").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  tracker issue or fix PR; earlier work on GC chains concerns other
  defects. The batching came with
  [ceph/ceph#46020](https://github.com/ceph/ceph/pull/46020). It is unfiled
  while filing is paused.
- **Found:** phase 1 unit W, Task 5, 2026-10-04, writing the GC enqueue;
  derived from the source, not reproduced.

## Tentacle's delete_objs_inline puts every ref through the first object's pool, without its locator

- **Kind:** defect, latent, not security-relevant: a regression in
  Tentacle. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`.
  - v20.2.4's `RGWRados::delete_objs_inline` opens one I/O context, on the
    pool of the chain's first object, sets full-try on it, and sends every
    object's refcount put through it (`driver/rados/rgw_rados.cc:6162-6171`
    and `:6178-6189` at v20.2.4). It moves each object's pool and locator
    into the `rgw_raw_obj` it hands the throttle, but `rgw::Aio`'s
    `librados_op` operates on `r.obj.oid` alone, through the context it was
    given (`rgw_aio.cc:61-63` and `:100` at v20.2.4), so neither is used: an
    object in another pool is looked for in the first one, and an object
    with a locator under its name alone.
  - v19.2.6 opens a context for each change of pool and sets each object's
    locator before its put (`driver/rados/rgw_rados.cc:5432-5457` at
    v19.2.6, the pool change at `:5439` and the locator at `:5451`).
    f39c5f50544, "rgw/rados: RGWRados::delete_objs_inline() uses
    AioThrottle", replaced that with the one context; it is first in
    v20.0.0, so every Tentacle release has it.
  - The function deletes inline the chain of an overwritten or deleted
    object whose GC enqueue failed (`:6118-6127` at v20.2.4) and multipart
    parts, from three call sites in `driver/rados/rgw_sal_rados.cc` at
    v20.2.4. Two clean up during multipart completion:
    `cleanup_orphaned_parts` (`:3929-3943`, reached from
    `RGWCompleteMultipart::execute`, `rgw_op.cc:7416`) and
    `cleanup_part_history` (`:3976-3990`, reached from `complete` at
    `:4421`, from `cleanup_orphaned_parts` at `:3925`, and from `abort` at
    `:4059`). Only the third, in `abort` (`:4066-4079`), deletes an aborted
    upload's parts.
  - As far as this reading finds, no chain radosgw builds today reaches the
    defect: one manifest's tails share its tail pool, which the function's
    own comment assumes ("RGWObjManifest uses the same pool for all tail
    objects", `driver/rados/rgw_rados.cc:6162-6163` at v20.2.4); an upload's
    parts share its placement; and their shadow and multipart namespace
    names carry no locator.
- **Impact:** none observed today, and a storage leak at worst. A chain
  that spans pools or holds an object with a locator would have its refs
  put on the wrong object or on none: a missed delete that orphans tails,
  never the deletion of a live object. The only such chain
  rgw-bug-reproduction found is a manifest with explicit objects, from
  before 2015.
- **Releases:** v20.2.4 and main. v19.2.6 is not affected.
- **rgw-go:** puts each object's ref through its own pool and locator, as
  v19.2.6 does (`deleteInline`, `internal/driver/gc_enqueue.go`), which
  sends the same requests as v20.2.4 for every chain radosgw builds
  (`docs/exclusions.md`, "Inline tail deletes run in parallel on Squid
  too").
- **Upstream:** none reports it. The regression came with f39c5f50544, in
  [ceph/ceph#59864](https://github.com/ceph/ceph/pull/59864), to speed up
  inline garbage collection; its commit names
  [#68134](https://tracker.ceph.com/issues/68134) as the issue it fixes. It
  is unfiled while filing is paused.
- **Found:** phase 1 unit W, Task 5 review, 2026-10-04, comparing the
  inline delete with v20.2.4; derived from the source, not reproduced.

## radosgw files a 404 on an existing bucket under the usage bucket "-", and never logs a missing bucket

- **Kind:** defect, not security-relevant, unfixed through main.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; a single line number is the same
  at v19.2.6 and v20.2.4, and a pair is v19.2.6's, then v20.2.4's.
  - `log_usage` files a request on a bucket under `s->bucket_owner`, and for
    any 404 replaces the bucket's name with `-`: "bucket not found, use the
    invalid '-' as bucket name" (`rgw_log.cc:207-208` and `:222-225`; the
    same lines on main at 6cafff02b39). Ceph commit c0074935d17 (2016), which
    added the `-`, states its purpose as logging "under the virtual error
    bucket '-' when bucket not found".
  - A missing bucket never reaches the usage log.
    `rgw_build_bucket_policies` returns `-ERR_NO_SUCH_BUCKET` before it sets
    `s->bucket_owner` (`rgw_op.cc:539-540` and `:552`, `:569-570` and
    `:582`; main `:649` and `:666`), so the entry's user is empty, and
    `RGWRados::log_usage` drops it with "user name empty ... skipping"
    (`driver/rados/rgw_rados.cc:1645-1648`, `:1748-1751`; main
    `:1836-1839`).
  - Every 404 on a bucket that exists is filed under `-` instead, with the
    bucket's owner as its user: NoSuchKey, NoSuchUpload,
    NoSuchLifecycleConfiguration, NoSuchBucketPolicy,
    NoSuchCORSConfiguration and NoSuchTagSet are all 404
    (`rgw_common.cc:97-137`, `:98-138`).
- **Impact:** a bucket's usage, as `radosgw-admin usage show --bucket` and
  the admin API report it, leaves out the bucket's 404 traffic, such as GETs
  and HEADs of missing keys and the error documents they send; it appears
  under the owner's bucket `-`, merged with the 404s of every other bucket
  of that owner. The requests the `-` was added for, those to a missing
  bucket, are not logged at all, and each flush that drops them logs a
  warning.
- **Releases:** v19.2.6, v20.2.4 and main.
- **rgw-go:** reproduces it, so that usage written by both gateways adds up
  under the same buckets: `op.LogUsage` files every 404 under `-` and logs
  nothing for a request whose bucket was not loaded
  (`internal/op/usagelog.go`), and the driver files an entry under the
  bucket it is given (`internal/driver/usage.go`).
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. The fix would scope `-` to the missing bucket. It is
  unfiled while filing is paused, and a report needs a reproduction on a
  running system.
- **Found:** phase 1 unit M, Task 10b, 2026-10-04, deciding whether rgw-go
  keeps `log_usage`'s `-`; derived from the source, not reproduced.

## radosgw files the first second of each hour's usage under the hour before

- **Kind:** defect, not security-relevant, unfixed through main.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`, and the lines are the same at v19.2.6
  and v20.2.4.
  - `UsageLogger::insert_user` files each entry under the hour its round
    timestamp starts, and moves that timestamp to the entry's hour only when
    `timestamp.sec() > round_timestamp + 3600` (`rgw/rgw_log.cc:139-143`; the
    same lines on main at 6cafff02b39).
  - `utime_t` has no `operator+` taking a number, so `round_timestamp`
    converts to `double` (`include/utime.h:230-232` and `:518-535`), and the
    test compares whole seconds and is strict.
  - So an entry stamped hh:00:00.x, the first second of an hour, while the
    round timestamp is at the hour before, is filed under the hour before;
    from hh:00:01 entries move to the new hour.
- **Impact:** the usage of requests that finish in the first second of an
  hour is reported in the previous hour's record whenever the round
  timestamp sits at that previous hour: after the gateway logged a request
  in it, or after the gateway started in it, since the constructor sets the
  round timestamp to the start time's hour (`rgw/rgw_log.cc:120-126`).
  `radosgw-admin usage show` with a start
  or end date on the hour counts them on the wrong side.
- **Releases:** v19.2.6, v20.2.4 and main.
- **rgw-go:** reproduces it (`Store.Log` in `internal/driver/usage.go`), so
  that both gateways file a request of the same second under the same hour.
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. The fix is `>=` for the strict `>`. It is unfiled while
  filing is paused, and a report needs a reproduction on a running system.
- **Found:** phase 1 unit M, Task 10b, 2026-10-04, transcribing
  `insert_user`; derived from the source, not reproduced.

## cls_rgw's usage reads and trims take in other users' records

- **Kind:** defect, not security-relevant: the half for ids beginning with
  `0` is fixed in v21.0.0, and the rest is unfixed through main.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's, then
  v20.2.4's.
  - `user_usage_log_add` stores each record under two omap keys of its usage
    object: by time, the epoch in 11 digits, `_`, the user, `_` and the
    bucket; and by user, the user, `_`, the epoch in 11 digits, `_` and the
    bucket (`cls/rgw/cls_rgw.cc:3536-3548`, `:3938-3950`; the method
    `:3563-3618`, `:3965-4020`).
  - A read or trim for one user scans the keys from its by-user prefix at the
    start epoch and stops at the first key that does not begin with the user
    and `_` (`cls/rgw/cls_rgw.cc:3636-3651` and `:3676-3681`, `:4038-4053`
    and `:4078-4083`). Another user whose id is that id, `_` and then a
    letter, `test_ops` for `test`, has by-user keys that begin so and sort
    after the start, so where both users' records share a usage object, as
    about one pair in `rgw_usage_max_shards` does (`usage_log_hash`,
    `rgw/driver/rados/rgw_rados.cc:1615-1628`, `:1718-1731`), the scan hands
    on the other user's records too, until it meets a record dated at or
    past the end. The trim
    callback removes the keys built from the record's own owner
    (`cls/rgw/cls_rgw.cc:3752-3768`, `:4154-4170`), so a trim for `test`
    deletes `test_ops`'s records.
  - A read or trim for all users scans from the by-time key of the start
    epoch to the first key not below the by-time key of the end epoch
    (`cls/rgw/cls_rgw.cc:3636-3637`, `:3645-3646` and `:3669-3674`,
    `:4038-4039`, `:4047-4048` and `:4071-4076`), and every by-user key in
    that range is decoded and handed on as well, so a read counts its record
    twice.
    When no end date is given, the end is 2^64-1 (`rgw/rgw_admin.cc:7691`,
    `rgw/radosgw-admin/radosgw-admin.cc:8077`; `rgw/rgw_rest_usage.cc:53`),
    whose key is `18446744073709551615`. By-time keys begin with `0` until
    2286, so such an undated range holds the by-user keys of every id
    beginning with `0`, and of ids beginning with `1` and a digit below `8`,
    such as `1000`. A dated end's key is `0` and ten more digits, so a dated
    range holds only the by-user keys that sort between its two keys, and
    not those of an id such as `0alice`.
  - Ceph commit 674d42d9023 (ceph/ceph#65329, in v21.0.0; see "cls_rgw usage
    trim never removes a payer-keyed record") puts `~` before the by-user key
    of an id beginning with `0` (main `cls/rgw/cls_rgw.cc:4146-4179` at
    6cafff02b39). Main still matches the by-user prefix as before
    (`:4322-4377`) and leaves ids beginning with `1` in an undated range.
- **Impact:** `radosgw-admin usage show --uid=test`, and the admin API's
  usage GET for `test`, report `test_ops`'s usage beside `test`'s whenever
  the two share a usage object, and `usage trim --uid=test`, or the admin
  API's usage DELETE for `test`, deletes it. A usage show for all users with
  no end date counts twice the usage of every user whose id begins with `0`,
  or with `1` and a digit below `8`. Only an administrator reaches either:
  usage trim and the all-users read need the usage caps, and the
  cross-user case also needs the two users' records to share one of
  `rgw_usage_max_shards` (default 32) usage objects and one id to be the
  other's followed by `_` and a suffix.
- **Releases:** v19.2.6 and v20.2.4; main for the prefix match and for ids
  beginning with `1`.
- **rgw-go:** writes the same two keys through `user_usage_log_add`, as it
  must for radosgw to read them (`internal/driver/usage.go`). It reads and
  trims no usage yet; unit N's usage read and trim send the same class
  methods and inherit the defect. fakerados's `Cluster.UsageEntries` tells
  the two keys apart by rebuilding each record's by-time key.
- **Upstream:** none for the prefix match and the double count.
  rgw-bug-reproduction's prior-art search found no report or fix; the nearest
  are [#72593](https://tracker.ceph.com/issues/72593), the intermingling of ids
  beginning with `0`, whose fix,
  [ceph/ceph#65329](https://github.com/ceph/ceph/pull/65329) (commit
  674d42d9023), is on main only, and
  [#80999](https://tracker.ceph.com/issues/80999), the payer and owner keys
  ("cls_rgw usage trim never removes a payer-keyed record"). It is unfiled while
  filing is paused, and a report needs a reproduction on a running system.
- **Found:** phase 1 unit M, Task 10b, 2026-10-04, emulating
  `user_usage_log_add`'s keys in fakerados; derived from the source, not
  reproduced.

## radosgw cuts X-Forwarded-For only for an rgw_remote_addr_param written in capitals

- **Kind:** defect, not security-relevant (low; triage estimate CVSS about
  4.2), unfixed through main.
- **Evidence:** paths are under `src/rgw/`.
  - `rgw_build_iam_environment` looks the variable `rgw_remote_addr_param`
    names up in the request's `RGWEnv`, whose map compares names without
    regard to case (`rgw_common.h:470` at v19.2.6; `rgw_env.cc:22-25` at
    both tags), so `http_x_forwarded_for` finds the X-Forwarded-For header.
  - It then cuts the value at its first comma only when the parameter
    equals `"HTTP_X_FORWARDED_FOR"` byte for byte (`rgw_op.cc:869-886` at
    v19.2.6, the test at `:878`; `:905-922` at v20.2.4, the test at
    `:913`).
  - With the parameter in any other capitals, `aws:SourceIp` is the whole
    proxy chain, `client, proxy1, ...`, which no address parses as, so an
    `IpAddress` condition on it never holds, and neither does
    `NotIpAddress`, which is false for a source that does not parse
    (`rgw_iam_policy.cc:971-992` at v19.2.6): a policy's IP-restricted
    Allow never grants, and its IP-restricted Deny never applies.
- **Impact:** an amplifier, not a bypass on its own: it bites only when
  X-Forwarded-For already carries a comma, in a deployment already open to
  a client that spoofs the header's leftmost address.
- **Releases:** v19.2.6 and v20.2.4, read at both tags, and main
  (`rgw_op.cc:1018` at 7ed73efc1be).
- **rgw-go:** reproduces it: the lookup ignores case and the cut needs the
  exact name (`sourceIP`, `internal/authz/env.go`), so a policy evaluates as
  on the zone's radosgw.
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no report or
  fix. Related: [#64489](https://tracker.ceph.com/issues/64489) (Fix Under
  Review), the spoofable leftmost X-Forwarded-For this amplifies, and
  [#23831](https://tracker.ceph.com/issues/23831). The fix is a case-insensitive
  test, or a cut of the value the lookup resolved. It is unfiled while filing is
  paused.
- **Found:** phase 1 unit Z, Task 9 and its review, 2026-10-05, reading
  `rgw_build_iam_environment` at both tags; derived from the source, not
  reproduced.

## radosgw ignores a public-access block that does not decode

- **Kind:** defect, not security-relevant (triage estimate CVSS about 2-3):
  a real fail-open that no ordinary path reaches.
- **Evidence:** paths are under `src/rgw/`.
  - `get_public_access_conf_from_attr` decodes a bucket's
    `user.rgw.public-access` attr and, on a `buffer::error`, returns
    `boost::none`, as for a bucket without the attr (`rgw_op.cc:340-355` at
    v19.2.6, `:370-385` at v20.2.4). Nothing logs it.
  - `rgw_build_bucket_policies` stores that as `s->bucket_access_conf`
    (`rgw_op.cc:585` at v19.2.6), so IgnorePublicAcls stops removing the
    public ACL grants (`rgw_common.cc:1419-1423`, `:1572-1575` at v19.2.6),
    PutObject and PutACLs stop refusing public canned ACLs and
    PutBucketPolicy stops refusing public policies (`rgw_op.cc:3904-3909`,
    `:5905-5906`, `:8103-8104` at v19.2.6), and on v20.2.4
    RestrictPublicBuckets stops refusing public policies
    (`rgw_common.cc:1374-1380`, `:1541-1547`).
  - The block narrows access, so dropping it widens access without a
    trace. radosgw refuses a request whose stored bucket policy does not
    parse instead (`rgw_op.cc:598-615` at v19.2.6, `:628-645` at v20.2.4).
  - radosgw writes the attr cleanly; it is reachable only through a
    corrupt attr or one another writer stored. No version of radosgw
    writes one another cannot read: the encoding is `ENCODE_START(1, 1)`
    over four Booleans at both tags (`rgw_public_access.h:21-25`, `:46`,
    `:55`), unchanged since the block came in, merged 2020-02-04. Any
    other writer must already be able to remove the block outright.
- **Releases:** v19.2.6 and v20.2.4, read at both tags, and main
  (`rgw_op.cc:414-428` at 7ed73efc1be).
- **rgw-go:** does not reproduce it: a block that does not decode refuses
  the request with AccessDenied (`publicAccess`, `internal/authz/load.go`;
  `docs/exclusions.md`, "A public-access block that does not decode refuses
  the request").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no report or
  fix. The fix would fail closed on `buffer::error`. The block came with
  [ceph/ceph#30033](https://github.com/ceph/ceph/pull/30033); related:
  [ceph/ceph#57206](https://github.com/ceph/ceph/pull/57206) and
  [#65741](https://tracker.ceph.com/issues/65741). It is unfiled while filing is
  paused.
- **Found:** phase 1 unit Z, Task 9 review, 2026-10-05; derived from the
  source, not reproduced.

## Squid's DeleteObjectTagging stamps the object with the epoch plus one nanosecond

- **Kind:** defect, fixed in Tentacle as a side effect of bucket logging.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; lines are v19.2.6's unless
  marked.
  - `RGWDeleteObjTags::execute` calls `delete_obj_attrs` on `s->object`
    without loading it first (`rgw_op.cc:1133-1139`). Its
    `verify_permission` loads it, through `rgw_iam_add_objtags` and
    `get_obj_attrs`, only when a policy carries an existing-object-tag or
    resource-tag condition (`rgw_op.cc:1117-1131` and `:727-731`).
  - The permission check reads the object's ACL through
    `rop->get_attr` (`get_obj_policy_from_attr`, `rgw_op.cc:288-302`), and
    that call fills only the object context's `RGWObjState`
    (`RadosObject::RadosReadOp::get_attr`,
    `driver/rados/rgw_sal_rados.cc:2875-2878`;
    `RGWRados::Object::Read::get_attr`, `driver/rados/rgw_rados.cc:6349-6362`),
    not the SAL object's own `state` (`StoreObject::state`,
    `rgw_sal_store.h:187`). Only `read_attrs` sets that `state.mtime`
    (`driver/rados/rgw_sal_rados.cc:2367-2372`).
  - `delete_obj_attrs` does not load the object either
    (`driver/rados/rgw_sal_rados.cc:2421-2429`), and `set_obj_attrs`, under
    the `FLAG_LOG_OP` it passes, nudges that zero mtime by a nanosecond
    (`:2378-2391`).
  - `set_attrs` takes any `set_mtime` other than zero as the object's
    mtime, writes it to the head with `mtime2`, and completes the index
    entry with it (`driver/rados/rgw_rados.cc:6683-6688`, `:6722-6725`).
  - v20.2.4 calls `get_obj_attrs` before `delete_obj_attrs`, for bucket
    logging (`rgw_op.cc:1351-1375` at v20.2.4; Ceph commit 12c201b2f94,
    "rgw/logging: support object metadata changes in journal mode", which
    v19.2.6 does not contain), so the nudge starts from the stored mtime.
- **Impact:** on Squid, a DeleteObjectTagging without such a policy
  condition sets the object's mtime to 1970-01-01T00:00:00.000000001, on the
  head and in its bucket index entry. GET and HEAD then report a
  Last-Modified of 1970, a listing reports the same, If-Modified-Since and
  If-Unmodified-Since compare against it, and lifecycle expiration by age
  takes the object as decades old (`rgw_lc.cc:1142-1156`). Not a security
  issue.
- **Releases:** v19.2.6. v20.2.4 is not affected.
- **rgw-go:** does not reproduce it. The DeleteObjectTagging op hands the
  driver's `SetObjectAttrs` the state its Init read, as v20.2.4 loads the
  object first, on both releases (`internal/op/objectattrs.go`), so the
  stored mtime is the one nudged. `docs/exclusions.md` records the
  difference from v19.2.6 ("DeleteObjectTagging keeps the object's mtime
  on Squid too").
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit W, Task 7 review, 2026-10-05, tracing the
  callers of `set_attrs`; derived from the source, not reproduced.

## radosgw never takes its OSD-filtered delimiter path

- **Kind:** defect, without a visible effect found. Unreproduced: derived
  from the source.
- **Evidence:** paths are under `src/`.
  - `rgw_cls_list_ret`'s decode sets `cls_filtered` for a reply of struct
    version 3 or later, the OSDs that roll names up to common prefixes
    themselves (`cls/rgw/cls_rgw_ops.h:436-443` and `:457` at v19.2.6,
    `:434-441` and `:455` at v20.2.4); Squid and Tentacle OSDs encode
    version 4.
  - `list_objects_ordered` starts its `cls_filtered` at false
    (`rgw/driver/rados/rgw_rados.cc:1817` at v19.2.6, `:1920` at v20.2.4),
    and `cls_bucket_list_ordered` only ANDs each shard's flag into it:
    `*cls_filtered = *cls_filtered && r.second.cls_filtered` (`:9783`,
    `:10703`). The flag therefore stays false, and the branch written for
    filtering OSDs (`:2024-2053`, `:2127-2156`) never runs.
  - The branch that does run, written for OSDs before version 16, rolls
    names up again on the client and moves the marker past each common
    prefix (`:2054-2085` and `:2109-2127` at v19.2.6, `:2157-2188` and
    `:2212-2230` at v20.2.4). For the single common-prefix entry a filtering
    OSD returns, it produces the same prefix, so the listing comes out the
    same. The two branches differ only in that the dead one would count a
    prefix that repeats across rounds each time, and skips the client's
    marker adjustment.
- **Impact:** none observed. Each listing redoes on the client the rollup
  the OSD has done, and the code path the comments describe as current is
  not the one that runs.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** takes the branch radosgw takes; `listObjectsOrdered`
  (`internal/driver/list.go`) keeps no filtered branch.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit M, Task 8, 2026-10-05, transcribing
  `list_objects_ordered`; derived from the source, not reproduced.

## radosgw's ListObjectsV2 with versions renders a malformed ListVersionsResult

- **Kind:** defect. Unreproduced: derived from the source.
- **Evidence:** `GET /<bucket>?versions&list-type=2` reaches
  `RGWListBucket_ObjStore_S3v2`, since `get_obj_op` chooses the handler by
  `list-type` alone (`rgw/rgw_rest_s3.cc:4610-4628` at v19.2.6,
  `:5151-5169` at v20.2.4) and both handlers read `versions` (`:1712`,
  `:1823`). Its
  `send_versioned_response` (`:1970-2051` at v19.2.6, `:2081-2162` at
  v20.2.4) differs from the v1 handler's (`:1799-1869`, `:1910-1980`):
  - a delete marker's section is named `DeleteContinuationToken`, where v1
    and S3 write `DeleteMarker` (`:1993-1994`);
  - the common prefixes are written twice: once before the versions, and
    again after them, each section then also carrying `KeyCount`, the
    number of versions, and `StartAfter` when sent (`:2033-2046`);
  - the key markers are `KeyContinuationToken` and
    `VersionIdContinuationToken`, and the next ones carry an empty version
    rather than `null` (`:1974-1979`);
  - `EncodingType` comes after the markers, so the first common prefixes
    are never URL-encoded (`:1981-1984`), and no version carries `Type`,
    nor `Owner` unless `fetch-owner` is true (`:2022-2024`).
- **Impact:** a client that sends `list-type=2` with `versions` gets a
  document no S3 schema describes. AWS's ListObjectVersions takes no
  `list-type`, so only a client that adds it meets this.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** renders the document as radosgw does, element for element
  (`internal/s3/listobjects.go`), so a client sees one shape from either
  gateway.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit M, Task 8, 2026-10-05, transcribing the S3
  listings; derived from the source, not reproduced.

## radosgw's listing checks a plain object named like a multipart meta object in the wrong pool

- **Kind:** defect. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`.
  - A bucket listing checks an index entry against its head when the entry
    has a pending op or is not there (`cls_bucket_list_ordered` and
    `cls_bucket_list_unordered`, `rgw/driver/rados/rgw_rados.cc:9823-9834`
    and `:10083-10091` at v19.2.6).
  - `check_disk_state` sets `in_extra_data` whenever `MultipartMetaFilter`
    accepts the entry's raw index name (`:10341-10344` at v19.2.6,
    `:11266-11268` at v20.2.4). The filter tests only that the name ends in
    `.meta` with a dot before it (`rgw/services/svc_tier_rados.cc:10-32`), so
    a plain object such as `notes.v1.meta` passes it as well as a multipart
    upload's `_multipart_<key>.<upload>.meta` does.
  - `in_extra_data` makes `get_head_data_pool` choose the placement's
    `data_extra_pool` (`rgw/driver/rados/rgw_zone.h:285-307` at v19.2.6,
    `:307-330` at v20.2.4), the non-EC pool under Rook, where the plain
    object's head is not. `get_obj_state` finds nothing, and the entry is
    dropped from the listing with a suggested REMOVE (`:10361-10377`), which
    `dir_suggest_changes` applies once no unexpired pending op remains
    (`cls/rgw/cls_rgw.cc:2283-2355` at v19.2.6).
  - Upstream already knows the filter misfiles such names. Ceph commit
    3cafe5774a5 ("rgw: avoid infinite loop when deleting a bucket", for
    [#49206](https://tracker.ceph.com/issues/49206), first in v17.1.0)
    replaced `MultipartMetaFilter` in `cls_bucket_list_unordered` with a
    test of the parsed namespace, saying the filter "may exclude data
    multiparts but include some regular objects with .meta suffix by
    mistake" (`rgw/driver/rados/rgw_rados.cc:10034-10038` at v19.2.6,
    `:10956-10960` at v20.2.4). `check_disk_state` was never changed to
    match.
- **Impact:** after a write of such an object leaves its index entry
  pending, for example when the gateway stops between the prepare and the
  completion, a listing removes the entry of an object that exists. The
  object stays readable by name but no longer lists, and the bucket's stats
  lose it.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** not affected. The driver's `checkDiskState`
  (`internal/driver/list.go`) treats only entries in the `multipart`
  namespace as upload meta objects and checks every other entry in the
  data pool (`docs/exclusions.md`, "A plain object whose name ends in .meta
  is checked where it is stored").
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit M, Task 8 review, 2026-10-05; derived from the
  source, not reproduced.

## A delimiter starting with an underscore hides the objects whose names start with one

- **Kind:** defect. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`.
  - cls_rgw's `bucket_list` searches for the delimiter in the raw index
    name, from the filter prefix's length, and rolls a match up to a
    common-prefix entry named up to and including the delimiter
    (`cls/rgw/cls_rgw.cc:625-663` at v19.2.6, `:675-713` at v20.2.4). An
    object whose name starts with `_` is indexed with the underscore
    doubled, so `_x` is `__x`.
  - With the delimiter `_` and no prefix, `__x` rolls up to the entry `_`.
    `list_objects_ordered` parses each entry's name with `parse_raw_oid`,
    which refuses a name shorter than `_x_` that starts with one underscore
    (`rgw/rgw_obj_types.h:285-310` at both tags), and skips the entry with
    "could not parse object name" (`rgw/driver/rados/rgw_rados.cc:1912-1917`
    at v19.2.6, `:2015-2020` at v20.2.4). Every object starting with `_`
    vanishes from the listing, where S3 returns the common prefix `_`.
  - With the delimiter `__`, the same object rolls up to the entry `__`,
    which parses as the object `_`. That name holds no `__`, so the listing
    returns it as an object named `_` with empty metadata, in place of the
    objects under it.
- **Impact:** a listing with a delimiter that starts with `_` loses or
  misnames every object whose name starts with `_`.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** reproduces it: the driver lists through the same class call
  and parse (`internal/driver/list.go`), and a driver spec pins both
  delimiters.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit M, Task 8 review, 2026-10-05; derived from the
  source, not reproduced.

## radosgw creates a multipart upload's meta object without the exclusive create its flags ask for

- **Kind:** defect, latent, not security-relevant: code hygiene, a dead
  flag and a retry loop that cannot run. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RadosMultipartUpload::init` asks for an exclusive create and retries a
    fresh upload id on EEXIST: it sets `PUT_OBJ_CREATE_EXCL` on the meta
    object's write (`driver/rados/rgw_sal_rados.cc:3304`, `:4148`) inside a
    `do ... while (ret == -EEXIST)` loop around `write_meta`
    (`:3277-3324`, `:4121-4170`).
  - The meta object is written through an `RGWObjectCtx` of init's own
    (`:3274`, `:4118`) that nothing sets atomic, so
    `prepare_atomic_modification` takes its non-atomic branch, which resets
    the object with `create(false)` and `remove_rgw_head_obj` whatever it
    finds (`driver/rados/rgw_rados.cc:6497-6505`, `:7334-7343`).
    `remove_rgw_head_obj` is the rgw class's `obj_remove`
    (`:5722-5727`, `:6410-6415`), which removes the object with
    `cls_cxx_remove`, its omap included (`cls/rgw/cls_rgw.cc:2410-2452`,
    `:2578-2620`).
  - Nothing in the RADOS driver makes the create exclusive for the flag at
    either tag. `PUT_OBJ_EXCL` is only defined
    (`driver/rados/rgw_rados.h:68-69`, `:70-71`) and set by init; the one
    test its bit changes is `_do_write_meta`'s `meta.flags ==
    PUT_OBJ_CREATE`, which applies the bucket's default retention and so
    skips the meta object (`driver/rados/rgw_rados.cc:3179`, `:3308`). A
    `create(false)` never fails with EEXIST, so the retry loop never runs.
  - An upload id that matched an in-progress upload's would therefore
    replace that upload's meta object: its `multipart_upload_info`, its
    attrs and the omap of part records, whose part objects no record then
    names.
- **Impact:** none in practice, and not a security issue. The id is
  generated by the gateway, not chosen by the client, and its random part
  is 31 characters of a 64-symbol table (`common/random_string.cc:45-50`
  at both tags), 186 bits, so two uploads of one key drawing the same id
  is not a practical event.
- **Releases:** v19.2.6 and v20.2.4; on main (7ed73efc1, 2026-09-25) init
  still sets the flag (`driver/rados/rgw_sal_rados.cc:4364`), which the
  driver names nowhere else.
- **rgw-go:** reproduces radosgw's write, `create(false)` and `obj_remove`
  without a guard (`CreateUpload`, `internal/driver/mp_upload.go`), so the
  meta object a shared zone sees is the one radosgw would write; its upload
  ids carry the same 186 random bits.
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no report or
  fix; [#44660](https://tracker.ceph.com/issues/44660) and
  [#50141](https://tracker.ceph.com/issues/50141) are unrelated. The code is the
  same on main.
- **Found:** phase 1 unit P, Task 3, 2026-10-05, transcribing
  CreateMultipartUpload's meta object write; derived from the source, not
  reproduced.

## radosgw keeps every modified bucket for good when its quota threads are off

- **Kind:** quirk, not security-relevant: radosgw keeps one entry per
  bucket it modified and never drains them while its quota threads are
  off, which is not the default; worth hardening, not a defect.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - Every write and delete updates the quota caches through
    `update_stats` (`rgw/driver/rados/rgw_rados.cc:3358-3366` and `:5984`,
    `:3511-3519` and `:6738`), and the owner cache's `adjust_stats` calls
    `data_modified` whether or not it holds the owner
    (`rgw/rgw_quota.cc:215-223`, the same lines at both tags).
  - `RGWOwnerStatsCache::data_modified` adds the bucket to
    `modified_buckets`, a `std::map` keyed by `rgw_bucket` (tenant, name and
    bucket id), only if it is not there yet (`rgw/rgw_quota.cc:346` and
    `:704-715`, `:725-736`), so it holds one entry per distinct bucket, not
    one per write.
  - Only `BucketsSyncThread` takes the set, swapping it out on each pass
    (`rgw/rgw_quota.cc:364` and `:467-470`, `:379` and `:488-491`), and
    that thread exists only with `rgw_enable_quota_threads`, which defaults
    to true (`rgw/rgw_quota.cc:488-495`, `:509-516`;
    `rgw/rgw_appmain.cc:235-237`, `:241-243`;
    `common/options/rgw.yaml.in:212-221` at v19.2.6).
  - So with the option off, the set keeps every bucket instance written to
    until the gateway restarts: a bucket deleted and recreated under the
    same name adds another, since its new instance has a new id.
- **Impact:** small and not induced by a client. A gateway run with
  `rgw_enable_quota_threads` off, as the option's description allows for
  all but one gateway per zone (`common/options/rgw.yaml.in:212-220`,
  `:218-226`), holds one map entry per bucket instance it wrote to since it
  started. That grows with distinct buckets, not with writes, and each
  user owns at most `rgw_user_max_buckets` of them at once (1000 by
  default, `rgw.yaml.in:2394-2401` at v19.2.6, `:2494-2501` at v20.2.4), so
  in an ordinary zone it stays well under a megabyte.
- **Releases:** v19.2.6, v20.2.4 and main (7ed73efc1be, 2026-09-25,
  `rgw/rgw_quota.cc:344`, `:507` and `:728-733`).
- **rgw-go:** keeps the modified buckets only while its bucket sync worker
  runs, which takes them on every pass (`Store.AdjustStats` in
  `internal/driver/stats.go`); nothing else reads them, so no behaviour
  differs.
- **Upstream:** none: with the premise of a defect refuted, there is
  nothing to file.
- **Found:** phase 1 unit M, Task 9, 2026-10-05, transcribing
  `RGWOwnerStatsCache`; derived from the source, not reproduced.

## Tentacle's DeleteObject removes the head without checking its write tag

- **Kind:** defect, a regression: a low-severity integrity defect (triage
  estimate CVSS about 2.2), found upstream first. Unreproduced: derived
  from the source.
- **Evidence:** paths are under `src/rgw/driver/rados/`.
  - v19.2.6's `Delete::delete_obj` calls `prepare_atomic_modification`
    with `removal_op` set (`rgw_rados.cc:5914` at v19.2.6), which appends a
    `cmpxattr` of `user.rgw.idtag` against the tag the delete read
    ("first verify that the object wasn't replaced under",
    `rgw_rados.cc:6493-6513` at v19.2.6) ahead of the head's `obj_remove`. A
    head another writer replaced since the read fails the removal with
    -ECANCELED; the index op is canceled and the request answers 204
    (`:5968-5972`; `rgw_op.cc:5302-5304` at v19.2.6).
  - Ceph commit 55f5b762c67, "RGW | fix conditional Delete and
    MultiDelete" (ceph/ceph#63348 on main; its picks f28f0d9147d through
    ceph/ceph#65949 on tentacle and ce98cebb815 through ceph/ceph#65932 on
    squid), replaced that call with `check_preconditions`, which only
    compares the state read before the op (`rgw_rados.cc:6663-6671` and
    `:7260-7329` at v20.2.4), and dropped `removal_op` from
    `prepare_atomic_modification`. Nothing else in the removal op checks
    the object: it is `obj_remove`, after `obj_check_mtime` when a time
    condition is set (`:6609-6624`, `:6665-6668`, `:6690-6699` at
    v20.2.4). The same code is on main (`rgw_rados.cc:7214-7250` at
    origin/main, 2026-10-04) and on the squid branch after v19.2.6.
- **Impact:** when a PUT replaces an object between a delete's read and its
  removal:
  - a delete with If-Match or `x-amz-if-match-size` naming the replaced
    object removes the replacement, whose ETag or size the condition does
    not name, so the conditional delete is not atomic;
  - the removal deletes the replacement's head, and
    `complete_atomic_modification` queues the tails of the state the delete
    read (`rgw_rados.cc:6715-6720` and `:6102-6130` at v20.2.4), which the
    PUT already queued, so no one queues the replacement's tails and they
    stay in the data pool, unreferenced, until an orphan scan finds them.

  Severity is low: the race window lies inside one request's handling,
  between its head read and its removal.
- **Releases:** v20.2.4, main, and the squid branch after v19.2.6, where
  ceph/ceph#65932 merged after v19.2.6 was tagged, so the next Squid release
  carries it too. v19.2.6 is not affected.
- **rgw-go:** unaffected: rgw-go keeps the guard on both releases
  (`internal/driver/delete.go`; `docs/exclusions.md`, "Delete conditions
  are Tentacle's, and a delete is guarded, on both releases").
- **Upstream:** found independently upstream and filed on 2026-09-26 as
  [#80898](https://tracker.ceph.com/issues/80898) (the replacement's tails leak) and
  [#80906](https://tracker.ceph.com/issues/80906) (a conditional delete that loses
  the race is answered success or 500), both Fix Under Review. The fix,
  which guards the head's removal on the id tag the delete read, is
  [ceph/ceph#72100](https://github.com/ceph/ceph/pull/72100), with
  [ceph/ceph#72096](https://github.com/ceph/ceph/pull/72096) (workunit tests) and
  [ceph/ceph#72101](https://github.com/ceph/ceph/pull/72101). The regression came from
  [ceph/ceph#63348](https://github.com/ceph/ceph/pull/63348), [ceph/ceph#65949](https://github.com/ceph/ceph/pull/65949) and
  [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932).
- **Found:** phase 1 unit W, Task 6, 2026-10-05, transcribing
  `delete_obj` at both tags; derived from the source, not reproduced.

## radosgw evaluates a tag-conditioned policy without the tags it fails to read

- **Kind:** defect. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`, cited at v19.2.6 and then
  v20.2.4.
  - Before it authorizes, an op whose policies name an
    `s3:ExistingObjectTag` or `s3:ResourceTag` key adds the object's or the
    bucket's tags to the IAM environment through `rgw_iam_add_objtags` and
    `rgw_iam_add_buckettags` (`rgw_op.cc:727-758`, `:757-788`).
  - `rgw_iam_add_objtags` returns the head read's error when
    `get_obj_attrs` fails (`:729-731`, `:759-761`), and
    `rgw_iam_add_tags_from_bl` returns -EIO, before adding a key, for a tag
    set that does not decode (`:708-725`, `:738-755`).
  - Every caller ignores the result and authorizes with the environment as
    it stands: RGWGetObj (`:987`, `:1142`), RGWGetObjTags (`:1045`,
    `:1244`), RGWPutObjTags (`:1085`, `:1284`), RGWDeleteObjTags (`:1126`,
    `:1344`), RGWGetACLs (`:5704`, `:6284`), and the others listed by
    `git grep rgw_iam_add_` at each tag. RGWPutACLs assigns the result to
    `op_ret` and still returns only the permission check's (`:5747-5756`,
    `:6325-6334`).
  - On a key the environment lacks, a condition holds only when it is
    IfExists or a ForAllValues operator (`rgw_iam_policy.cc:861-868`,
    `:881-888`); a Null test answers that the key is absent (`:857-859`,
    v20.2.4 compares that answer with its values, `:876-879`). So a Deny
    such as "Deny s3:DeleteObject if s3:ExistingObjectTag/protected = true"
    is skipped when the read fails, whether from a transient RADOS error or
    a damaged attr, and every tag condition is decided as if the object
    carried no tags.
- **Trigger:** a request whose bucket or identity policy names a tag key,
  on an object whose head read fails with anything but ENOENT, or on an
  object or bucket whose `user.rgw.x-amz-tagging` attr does not decode.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** phase 1 (unit Z) refuses such a request with 500
  InternalError, which `op.Run` never overrides; a missing object still
  adds no tags. That is a difference from radosgw, which
  `docs/exclusions.md` records.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit Z, Task 10 review, 2026-10-05; derived from the
  source, not reproduced.

## A failed UploadPart shrinks radosgw's quota cache

- **Kind:** defect: quota-cache integrity, low (triage estimate CVSS about
  4.3-5.3), and not a disclosure. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - A part's first stripe is created by `RadosWriter::write_exclusive`,
    which adds the part head to the writer's `written` set
    (`driver/rados/rgw_putobj_processor.cc:161-177`, `:189-205`). The head
    is counted in the quota cache only later, by the part head's
    `write_meta` in `MultipartObjectProcessor::complete`, through
    `update_stats(owner, bucket, 1, accounted_size, 0)`
    (`driver/rados/rgw_rados.cc:3364-3366`, `:3517-3519`).
  - `RGWPutObj::execute` returns before `complete` when the body ends
    short, the second quota check fails, the Content-MD5 does not match,
    the aws-chunked or SigV4 payload check fails, or the body passes
    `rgw_max_put_size` (`rgw_op.cc:4430-4486`, `:4662-4718`). The
    processor's `~RadosWriter` then removes what it wrote, and for a part
    head in the pool the bucket's placement rule names it calls
    `RGWRados::delete_obj` (`driver/rados/rgw_putobj_processor.cc:184-229`,
    `:212-257`; the call at `:224`, `:252`).
  - `delete_obj`'s unversioned path reads the head's state, whose
    `accounted_size` is the head's own size (`driver/rados/rgw_rados.cc:6173`,
    `:6927`), removes it through the index and ends with
    `update_stats(bucket_owner, bucket, -1, 0, obj_accounted_size)`
    (`:5984`, `:6738`). `RGWQuotaHandler::update_stats` applies that to
    both the bucket's and the owner's cached stats, clamping at zero
    (`rgw_quota.cc:957-960`, `:978-981`; `RGWQuotaStatsUpdate`,
    `:175-212` at both tags).
  - Nothing added the head, so each such request subtracts one object and
    up to one stripe, `rgw_obj_stripe_size` (4 MiB by default), that the
    cache never held.
- **Impact:** the trigger is any client allowed to UploadPart into the
  bucket, with a part of any size: the end of the body flushes the part's
  first stripe through `write_exclusive` (`rgw_op.cc:4425`, `:4657`), and a
  Content-MD5 the client chose to mismatch then fails the request with
  BadDigest before `processor->complete` (`:4483-4486`, `:4715-4718`), so
  even a 1-byte part lowers the cached object count of the bucket and of
  its owner by one. Four things limit it:
  - the cache clamps each value at zero (`RGWQuotaStatsUpdate::update`,
    `rgw_quota.cc:192-208` at both tags);
  - only the cache moves: the index's own stats, which the cache is
    fetched from again, are untouched, as cls_rgw unaccounts only an entry
    that exists (`unaccount_entry`, `cls/rgw/cls_rgw.cc:887-898`,
    `:1017-1028`), and the part head never had one;
  - one attempt moves the cached size by at most one stripe,
    `rgw_obj_stripe_size` (4 MiB by default);
  - the cache heals itself: a lookup at least `rgw_bucket_quota_ttl`/2
    (300 s by default) after the entry was fetched starts a refresh from the
    index, and the entry expires at `rgw_bucket_quota_ttl`
    (`rgw_quota.cc:132-155` at both tags).

  Within that window the quota checks see less usage than there is, so a
  bucket or user near its quota can take more than the quota allows. Each
  gateway caches on its own.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. It removes the part head through the
  index as radosgw does, but subtracts it from the quota cache only when
  the head's write_meta had added the part (`removePartHead`,
  `internal/driver/mp_part.go`; `docs/exclusions.md`, "A failed part
  upload does not shrink the quota cache").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. It is unfiled while filing is paused.
- **Found:** phase 1 unit P, Task 4, 2026-10-05, transcribing
  `~RadosWriter`'s removal of a part head; derived from the source, not
  reproduced.

## Tentacle fails an UploadPart that sends If-None-Match: * for a part in the bucket placement's pool

- **Kind:** defect, not security-relevant: it fails closed. It is the
  Tentacle side of the conditional-write work upstream, so it was not found
  by us. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/` at v20.2.4.
  - `RGWPutObj_ObjStore_S3::get_params` takes If-Match and If-None-Match
    for every PUT, UploadPart included (`rgw_rest_s3.cc:2783-2784`), and
    `RGWPutObj::execute` hands them to the part's processor
    (`rgw_op.cc:4836-4838`).
  - v20.2.4's `MultipartObjectProcessor::complete` sets them on the part
    head's `write_meta` (`driver/rados/rgw_putobj_processor.cc:563-564`);
    v19.2.6's sets neither (`:491-531` at v19.2.6).
  - With a condition set, `write_meta` skips the assume-no-entry pass and
    reads the part head's state (`driver/rados/rgw_rados.cc:3583-3592`),
    from the pool the bucket's placement rule names
    (`get_obj_state_impl`, `:6899`). There the request's own exclusive
    create has already put the part head (`RadosWriter::write_exclusive`,
    `driver/rados/rgw_putobj_processor.cc:189-205`).
  - `check_preconditions` then fails If-None-Match: * on that head with
    412 PreconditionFailed, and If-Match naming an ETag too, since a part
    head has no ETag until this write sets it (`driver/rados/rgw_rados.cc:3294`,
    `:7286-7322`). `~RadosWriter` removes the part.
  - For a part whose storage class lives in another pool the state read
    finds nothing, so If-None-Match: * passes, while If-Match, `*` or an
    ETag, fails with 404 NoSuchKey: `check_preconditions` answers ENOENT
    for a missing object (`driver/rados/rgw_rados.cc:7287-7305`).
- **Impact:** on Tentacle, an UploadPart of a part in the bucket placement's
  pool that sends If-None-Match: * or If-Match with an ETag always fails,
  after the part's body has been received and written, and a part of a
  storage class in another pool fails on any If-Match instead; on Squid
  every such request succeeds. The outcome depends on the upload's storage
  class, not on any state the client can see.
- **Releases:** v20.2.4; v19.2.6 ignores the conditions.
- **rgw-go:** does not reproduce it: a part write takes no conditions on
  either release (`PutPart`, `internal/driver/mp_part.go`;
  `docs/exclusions.md`, "UploadPart ignores If-Match and If-None-Match").
- **Upstream:** the conditions reach the part head through the conditional-write
  fix for [#68183](https://tracker.ceph.com/issues/68183),
  [ceph/ceph#63348](https://github.com/ceph/ceph/pull/63348), with its backports
  [ceph/ceph#65949](https://github.com/ceph/ceph/pull/65949) (tentacle, in
  v20.2.4) and [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932)
  (squid, merged after v19.2.6). Related:
  [#80907](https://tracker.ceph.com/issues/80907) and
  [#80925](https://tracker.ceph.com/issues/80925).
- **Found:** phase 1 unit P, Task 4, 2026-10-05, comparing
  `MultipartObjectProcessor::complete` at the two tags; derived from the
  source, not reproduced.

## radosgw's UploadPartCopy can assemble a part from two versions of its source

- **Kind:** defect, security-relevant: a silent loss of integrity (triage
  estimate CVSS about 5.3). Unreproduced: derived from the source and a
  compiled model of the copy loop.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWPutObj::execute` copies a part's source range in pieces of
    `rgw_max_chunk_size`, calling `get_data(fst, cur_lst, data)` for each
    (`rgw_op.cc:4394`, `:4626`).
  - Each call builds a new source object, whose `RadosObject` has an
    `RGWObjectCtx` of its own (`driver/rados/rgw_sal_rados.h:536-544`,
    `:543-551`), and runs `read_op->prepare` on it, reading the source's
    head and manifest again, before it iterates the piece
    (`rgw_op.cc:4018-4085`, `:4227-4294`; the prepare at `:4039`, `:4248`).
  - Only the source's first state, read before the loop, set the range;
    nothing compares a later piece's state with it. The copy-source
    conditions that would pin a version are read only for CopyObject
    (`RGWCopyObj_ObjStore_S3::get_params`, `rgw_rest_s3.cc:3478` and
    `:3513`, `:3758` and `:3793`), not for UploadPartCopy.
- **Impact:** a source object overwritten while a part copies from it
  yields a part whose leading pieces come from the old object and the rest
  from the new one: data that was never any version of the source, under
  an ETag computed over it. A shorter new version that is not empty fails
  the copy with 416 InvalidRange once a piece starts past its end, after the
  leading pieces were written: each piece's `range_to_ofs` refuses an offset
  at or past the object's size with ERANGE (`rgw_op.cc:4072`, `:4281`;
  `rgw_sal.cc:429-449`, `:426-446`). An emptied source skips that check,
  reads nothing and ends the copy, and the part is stored short without an
  error, since a copy's expected length grows with what each piece read
  (`rgw_op.cc:4398`, `:4630`). The part's ETag is the MD5 of the bytes
  it holds, so CompleteMultipartUpload accepts it, and nothing tells the
  uploader. A principal who controls the source can time the overwrite,
  so the mix is not only chance.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. `CopyPart` reads the whole range from
  the one state the op read, through the read path, whose head reads that
  state's write tag guards, so the part holds that version or the copy
  fails (`internal/driver/mp_part.go`;
  `docs/exclusions.md`, "UploadPartCopy reads one version of its source").
  The UploadPartCopy op checks `x-amz-copy-source-if-match`,
  `-if-none-match`, `-if-modified-since` and `-if-unmodified-since` against
  that same state before it copies, as CopyObject checks them, so a client
  can pin the version it copies (`internal/op/uploadpart.go`;
  `docs/exclusions.md`, "UploadPartCopy checks the copy-source
  conditions").
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-07; awaiting response. rgw-bug-reproduction's prior-art search
  found no report or fix; it is distinct from the public overwrite reports
  [#80896](https://tracker.ceph.com/issues/80896),
  [#80898](https://tracker.ceph.com/issues/80898),
  [#80899](https://tracker.ceph.com/issues/80899),
  [#80900](https://tracker.ceph.com/issues/80900) and
  [#80906](https://tracker.ceph.com/issues/80906). It is unfiled while
  filing is paused.
- **Found:** phase 1 unit P, Task 4, 2026-10-05, transcribing
  UploadPartCopy's read of its source; derived from the source, not
  reproduced.

## radosgw registers a part whose head write lost a race and then removes its data

- **Kind:** defect, latent, not security-relevant: the register-then-remove
  branch is dead defensive code. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `_do_write_meta` answers a head write that fails with ECANCELED,
    ENOENT or EEXIST, without conditions, as a lost race: it cancels the
    index entry, sets `meta.canceled` and returns 0
    (`driver/rados/rgw_rados.cc:3369-3391`, `:3522-3544`).
  - `MultipartObjectProcessor::complete` goes on after that 0: it
    registers the part on the meta object with
    `cls_rgw_mp_upload_part_info_update`, and only then, finding the write
    canceled, skips `writer.clear_written()`
    (`driver/rados/rgw_putobj_processor.cc:530-606`, `:566-645`), so
    `~RadosWriter` removes the part head and its stripes (`:184-229`,
    `:212-257`).
  - The upload then holds a part record whose objects are gone, and a
    completion that lists the part writes an object whose manifest names
    them.
- **Impact:** none known in practice. The part head write is never set
  atomic and carries no create and no conditions, so it has no guard that
  ECANCELED or EEXIST would come from, and its setxattr and class call
  create the object rather than fail with ENOENT. The path needs an OSD
  reply no known failure gives. Each part also has a head oid of its own,
  so no other write races it.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it: a canceled part head write removes
  the part's objects and fails the UploadPart without registering it
  (`registerPart`, `internal/driver/mp_part.go`; `docs/exclusions.md`, "A
  part whose head write lost a race is not registered").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix; it is distinct from #80896 to #80907. The fix would
  register the part only when the write was not canceled; it is a heads-up
  for upstream, not a report, once filing resumes.
- **Found:** phase 1 unit P, Task 4, 2026-10-05, transcribing
  `MultipartObjectProcessor::complete`; derived from the source, not
  reproduced.

## radosgw's gc removes queue entries by count, so a pass drops entries not yet due that precede due ones

- **Kind:** defect, unfixed through main: an operational leak, gated on
  the operator's configuration, not security-relevant. Unreproduced:
  derived from the source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWGC::process` lists a converted shard's queue for the expired
    entries only and, once the page's refcount puts succeed, removes
    `entries.size()` entries (`rgw/driver/rados/rgw_gc.cc:617` and `:710`,
    `:632` and `:722`).
  - `cls_rgw_gc_queue_list_entries` counts every entry it scans that is not
    deferred toward the page, but returns only those already due, and does
    not stop at the first entry not yet due, though its comment says it
    could, "since all subsequent entries won't have expired"
    (`cls/rgw_gc/cls_rgw_gc.cc:192-201` at both tags).
  - `cls_rgw_gc_queue_remove_entries` removes that many entries from the
    queue's front, whatever their due time (`:261` and `:332-347` at both
    tags).
  - The queue keeps enqueue order, and each entry is due at the OSD's
    `real_clock::now()` plus the request's `expiration_secs` (`:71-72`), so
    an entry enqueued earlier is due later when the two enqueues carried
    different `rgw_gc_obj_min_wait` values (gateways configured
    differently, or a changed setting) or the OSD's clock stepped back
    between them.
  - A pass over [A, not yet due; B, due] then frees B's objects and removes
    A. A's objects are never freed. B stays queued until a later pass lists
    it again, whose puts answer ENOENT, which counts as done.
- **Impact:** the tails of the dropped entry leak, with no log line; live
  data is never freed early: the dropped entry's head is already gone. Only
  an operator reorders the queue, by lowering `rgw_gc_obj_min_wait` or
  running gateways with different values; no client can, since the GET
  path's `defer_gc`, which pushed an entry's due time later, is disabled
  ([#47866](https://tracker.ceph.com/issues/47866)).
- **Releases:** v19.2.6, v20.2.4 and main (7ed73efc1be, 2026-09-25, the
  same code in both files).
- **rgw-go:** meets it as radosgw does, on purpose, since both gateways
  share one queue and its class's semantics: its gc worker sends the same
  listing and removal (`internal/driver/gc.go`), and a spec pins the
  dropped entry. `docs/exclusions.md` has no entry for it.
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix; the gc stall reports
  ([#77098](https://tracker.ceph.com/issues/77098) among them) are
  distinct. The fix would stop the listing at the first entry not yet due,
  or remove entries up to the marker of the last one processed.
- **Found:** phase 1 unit W, Task 9, 2026-10-05, transcribing
  `RGWGC::process`; derived from the source, not reproduced.

## radosgw's gc worker runs its passes without pause on a non-positive rgw_gc_processor_period

- **Kind:** defect, unfixed through main: operational, gated on the
  operator's configuration, not security-relevant. Unreproduced: derived
  from the source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `rgw_gc_processor_period` and `rgw_gc_processor_max_time` are `int`
    options with no `min:` (`common/options/rgw.yaml.in:1752` and `:1730`,
    `:1840` and `:1818`).
  - `GCWorker::entry` reads the period into an `int`, skips the wait when
    it is not more than the whole seconds the pass took, and otherwise
    waits the difference (`rgw/driver/rados/rgw_gc.cc:795-805`,
    `:807-817`). A period of 0 or below therefore never waits: the worker
    runs pass after pass, each taking the `gc_process` lock on and listing
    every gc shard.
  - `RGWGC::process` reads the max time into an `int` too (`:731`, `:743`).
    Both reads narrow the 64-bit option to 32 bits, so a value of 2^31
    seconds or more can turn negative: a max time that does refuses every
    pass with -EAGAIN (`:564-565`, `:579-580`), and a period that does
    stops the waits as above.
- **Impact:** with a period of 0 or below, every gateway running the gc
  thread loads the gc pool's OSDs without pause; values past 2^31 seconds
  only by mistake.
- **Releases:** v19.2.6, v20.2.4 and main (7ed73efc1be, 2026-09-25,
  unchanged).
- **rgw-go:** narrows both options as radosgw does, but waits a second
  after a pass when the period is not positive and logs an error naming the
  option (`internal/driver/gc.go`; `docs/exclusions.md`, "A gc processor
  period that is not positive").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix; the fix would clamp the period to a floor. Related:
  [#81226](https://tracker.ceph.com/issues/81226) reports the same class of
  defect for the usage-log and quota intervals.
- **Found:** phase 1 unit W, Task 9, 2026-10-05, transcribing
  `GCWorker::entry`; derived from the source, not reproduced.

## radosgw's gc processor faults on a chain object without a pool

- **Kind:** defect, latent, unfixed through main: robustness, not
  security-relevant, since no client can create a chain object without a
  pool. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWGC::process` creates one I/O context per shard pass,
    `IoCtx *ctx = new IoCtx`, which no pool opens
    (`rgw/driver/rados/rgw_gc.cc:583`, `:598`; `librados::IoCtx::IoCtx`
    sets `io_ctx_impl` to null, `librados/librados_cxx.cc:1108`, `:1115`).
    It replaces the context only when an object's pool differs from
    `last_pool`, and `last_pool` starts empty on every page (`:633` and
    `:660-674`, `:648` and `:672-686`). After a failed open on an
    unconverted shard it recreates the context unopened and empties
    `last_pool` again (`:661-672`, `:673-684`).
  - An object whose `pool` is empty therefore keeps whatever context the
    pass holds. On the shard's first page before any pool opened, or right
    after a failed open, that context is unopened: `locator_set_key` and
    `set_pool_full_try` (`:676-677`, `:688-689`) dereference the null
    `io_ctx_impl` (`librados/librados_cxx.cc:2262-2265` and `:2337-2340`,
    `:2269-2272` and `:2344-2347`), and the process faults.
  - On a later page the context is the previous page's last opened pool,
    so the refcount put goes to that pool under the object's name. It
    usually answers ENOENT, which counts as done (`:413-415`, `:428-430`),
    so the entry is removed, or its tag counted down, while the object it
    named is never freed.
  - `update_gc_chain` records an empty pool for a stripe whose placement
    resolves no pool (`rgw/driver/rados/rgw_rados.cc:5409-5421`, `:6132-6144`),
    because `rgw_obj_select::get_raw_obj` ignores `obj_to_raw`'s failure
    (`:148-156`, `:156-164`).
  - v19.2.6's `delete_objs_inline` has the same shape: `last_pool` starts
    empty, so an empty pool first in the chain skips the open and
    `locator_set_key` meets an unopened context
    (`rgw/driver/rados/rgw_rados.cc:5434-5451` at v19.2.6).
- **Impact:** a process crash in the gc thread on every pass that reaches
  such an entry on a shard's first page before any pool opened, or, on a
  shard still on the omap log, right after a failed open (a shard moved to
  the queue ends its pass on a failed open instead, `rgw_gc.cc:665-667`,
  `:677-679`). The entry stays queued, so every gateway that takes that
  shard's lock crashes in turn. Reached on a later page, once an earlier
  page opened a pool, it is a put to the wrong pool and a leaked tail.
  Only an object whose placement no longer resolves when it is overwritten
  or deleted gets such an entry: a removed placement or storage class,
  placement skew between gateways, or a corrupt or old manifest.
- **Releases:** v19.2.6, v20.2.4 and main (7ed73efc1be, 2026-09-25, the
  same `IoCtx` and `last_pool` handling).
- **rgw-go:** opens the pool of a page's first object, and of an object
  after a failed open, by its name, so the empty name fails as a missing
  pool does, on every page, and fails gracefully (`internal/driver/gc.go`;
  `docs/exclusions.md`, "The gc worker opens a chain object's pool even
  when it has none").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no report or
  fix. It is distinct from [#68169](https://tracker.ceph.com/issues/68169), the
  stall on a pool that does not exist, whose fix,
  [ceph/ceph#59902](https://github.com/ceph/ceph/pull/59902), adds no guard for
  an empty pool. A robustness fix is worth filing once filing resumes.
- **Found:** phase 1 unit W, Task 9, 2026-10-05, transcribing
  `RGWGC::process`; derived from the source, not reproduced.

## radosgw's gc stalls a converted shard behind an entry it cannot free

- **Kind:** defect; main fixes one cause. Operational, not
  security-relevant: no client can plant an entry that cannot be freed.
  Not found by us: it is the public gc stall
  ([#68169](https://tracker.ceph.com/issues/68169),
  [#77098](https://tracker.ceph.com/issues/77098)). Unreproduced here:
  derived from the source.
- **Evidence:** paths are under `src/rgw/driver/rados/`; each pair of
  lines is v19.2.6's, then v20.2.4's.
  - On a shard moved to the rgw_gc queue, a pool that cannot be opened, a
    put that cannot be scheduled, or a put that fails with anything but
    ENOENT ends the shard's pass before the page's entries are removed
    (`rgw_gc.cc:665-667`, `:690-692` and `:703-707`; `:677-679`,
    `:702-704` and `:715-719`). The next pass lists the same page first.
  - So one entry whose object can never be freed, as when its data pool was
    deleted after it was queued or the gateway's key cannot write that
    pool, stops the shard's collection for good. New entries pile up behind
    it until the queue reaches `rgw_gc_max_queue_size`, after which the
    enqueue fails and overwrites delete their tails inline.
  - Two lesser cases delay collection by a pass. `schedule_io` and
    `drain_ios` judge a completion by the shard being processed, not the
    one the operation belongs to, so a failed put an earlier, unconverted
    shard left in flight ends a converted shard's pass (`:384-392` and
    `:703-707`, `:399-407` and `:715-719`). And an unlock that failed leaves
    the gateway's own lock, which its next pass meets as EEXIST before the
    lock expires, ending the whole pass (`:571-578` and `:739-741`,
    `:586-593` and `:751-753`).
  - Main skips a pool that answers ENOENT instead
    (`if (ret != -ENOENT && transitioned_objects_cache[index])`,
    `rgw_gc.cc` at 7ed73efc1be, 2026-09-25), which frees a shard stalled by
    a deleted pool; a put that keeps failing still stalls it.
- **Impact:** a gc shard that stops collecting, its tails leaking, with an
  error logged on each pass.
- **Releases:** v19.2.6 and v20.2.4; main for the persistent put failure.
  The squid branch does not carry main's fix as of a742f50616e
  (2026-09-03); its cherry-pick, 7b77cbc4466, is only on the umbrella
  branches.
- **rgw-go:** meets it as the floor releases do: on a queue shard its gc
  worker removes a page's entries only once every put of the page
  succeeded, and a pool it cannot open, a deleted one included, a put it
  cannot schedule or one that fails ends the pass before the removal
  (`freeEntries` and `shardPass`, `internal/driver/gc.go`).
  `docs/exclusions.md` has no entry for it.
- **Upstream:** [#68169](https://tracker.ceph.com/issues/68169)
  (Backporting) reports the deleted pool, and
  [#77098](https://tracker.ceph.com/issues/77098) the stall behind an entry
  that cannot be freed. Main's fix is
  [ceph/ceph#59902](https://github.com/ceph/ceph/pull/59902) (merge
  ccd9d3e94bc, 73535b105de). rgw-bug-reproduction's triage reports a squid
  backport, [ceph/ceph#70395](https://github.com/ceph/ceph/pull/70395), as
  merged, and a tentacle backport,
  [ceph/ceph#70396](https://github.com/ceph/ceph/pull/70396), as open.
- **Found:** phase 1 unit W, Task 9, 2026-10-05, transcribing
  `RGWGC::process` and `RGWGCIOManager`; derived from the source, not
  reproduced.

## radosgw's gc takes a failed omap listing of a converted shard for an empty one

- **Kind:** defect, unfixed through main: a leak that heals at restart,
  not security-relevant. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/driver/rados/`; each pair of
  lines is v19.2.6's, then v20.2.4's.
  - On a shard it has not yet seen converted, `RGWGC::process` lists the
    omap log (`rgw_gc.cc:591`, `:606`), reads the shard's cls version, and
    on version 1 with no entries lists once more for any entry at all
    (`:600`, `:615`); when that finds none it marks the shard converted
    (`:601-602`, `:616-617`; the whole block `:590-614`, `:605-629`).
  - A listing that fails returns no entries, and the second listing's
    result is not checked before the decision, so that one failed listing,
    a timeout or an EIO, marks a shard converted while its omap log still
    holds entries. The first listing is unchecked too, but its failure
    only leads to the second. From then on the gateway lists only the
    queue and never frees those entries' objects.
  - The mark is an in-memory `std::vector<bool>` that
    `RGWGC::initialize` fills with false (`rgw_gc.h:52` and
    `rgw_gc.cc:45-46` at v19.2.6), so a restart lists the omap log again.
  - `RGWGC::list`, which the admin's gc listing runs, has the same
    unchecked second listing before the same mark (`rgw_gc.cc:274-278`,
    `:288-292`).
  - A shard at version 1 holds omap entries only from before
    `RGWGC::initialize` converted it, or from enqueues the queue refused
    with ECANCELED or EPERM (`RGWGC::send_chain`, `:120-139` at both
    tags).
- **Impact:** a leak, until restart, of the tails named by a converted
  shard's leftover omap entries, mostly its backlog from before the
  conversion; it needs one failed read at the wrong moment.
- **Releases:** v19.2.6, v20.2.4 and main (7ed73efc1be, 2026-09-25,
  unchanged).
- **rgw-go:** meets it as radosgw does: its gc worker marks the shard
  converted when the second listing returns no entries, without checking
  that listing's error, and keeps the mark in memory for the worker's life
  (`shardPass`, `internal/driver/gc.go`).
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. The fix would end the pass when the second listing fails,
  `if (ret < 0) goto done;`, before the mark.
- **Found:** phase 1 unit W, Task 9, 2026-10-05, transcribing
  `RGWGC::process`; derived from the source, not reproduced.

## A negative or overlong rgw_gc_obj_min_wait makes radosgw's gc free overwritten tails at its next pass

- **Kind:** defect, unfixed through main: robustness, gated on the
  operator's configuration and not reachable by a client. Unreproduced:
  derived from the source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `rgw_gc_obj_min_wait` is an `int` option with no `min:`
    (`common/options/rgw.yaml.in:1711`, `:1799`), the wait that lets
    readers of an overwritten or deleted object finish before its tails go.
    radosgw passes it to every enqueue as a `uint32_t` expiration
    (`rgw/driver/rados/rgw_gc.cc:126` and `:137` at both tags;
    `rgw/driver/rados/rgw_gc_log.cc:29-30` and
    `cls/rgw_gc/cls_rgw_gc_client.cc:50` at both tags), so -1 arrives as
    2^32-1 seconds.
  - The class sets the entry's due time to `real_clock::now()` plus that
    (`cls/rgw_gc/cls_rgw_gc.cc:71-72`; `cls/rgw/cls_rgw.cc:3912-3913`,
    `:4314-4315`) and stores it in 32-bit seconds: `encode` of a
    `real_time` keeps the low 32 bits (`include/encoding.h:346-354`,
    `:347-355`), and the omap log's time key goes through `ceph_timespec`,
    whose `tv_sec` is `__le32` (`cls/rgw/cls_rgw.cc:119-125`, `:135-141`;
    `include/rados.h:42-45` at both tags).
  - Today's time, about 1.79e9 seconds, plus anything from about 2.5e9 to
    2^32-1 seconds wraps past 2106 to a time already gone. So a value from
    -1 down to about -1.79e9, a positive one above about 2.5e9 (79 years),
    or one of 2^32 or more whose low 32 bits are that large, queues the
    tails due at once, and the next gc pass frees them; a value further
    below -1.79e9, or one whose low 32 bits are small, wraps to a due time
    decades away or seconds away instead.
- **Impact:** the tails of an overwritten or deleted object can be freed
  while a GET that started before the change still reads them, and the GET
  fails part-way. Only a misconfigured option reaches it: a negative wait,
  or one meant as "practically never" that is long enough to wrap.
- **Releases:** v19.2.6, v20.2.4 and main (7ed73efc1be, 2026-09-25,
  `include/encoding.h:396`, unchanged).
- **rgw-go:** never sends a wait that wraps (`docs/exclusions.md`, "A
  negative or overlong gc object wait"). `readWriteOptions`
  (`internal/driver/writer.go`) uses the default, 7200 s, for a negative
  value and logs an error for either case; each enqueue sends the wait
  bounded by `gcExpiration`, which keeps the due time a day short of 2106
  by the gateway's clock (`internal/driver/gc_enqueue.go`). Specs pin -1
  and the most negative int64 to 7200 s, and the most positive int64 and
  the first wait that would overflow to a due time a day short of 2106,
  through the gc worker, which leaves those tails in place.
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. The fix would bound the wait at zero and encode the due
  time in 64 bits; a robustness report once filing resumes.
- **Found:** phase 1 unit W, Task 9, 2026-10-05, pinning a negative
  `rgw_gc_obj_min_wait` through the gc worker; derived from the source, not
  reproduced.

## radosgw's gc can remove a queue page twice when a pass outlives its shard lock

- **Kind:** defect, unfixed through main: robustness, not
  security-relevant. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/driver/rados/`; each pair of
  lines is v19.2.6's, then v20.2.4's.
  - `RGWGC::process` takes the shard's `gc_process` lock for
    `rgw_gc_processor_max_time` seconds (`rgw_gc.cc:567-571`, `:582-586`),
    and only the check before each entry bounds the pass by that time
    (`:645-648`, `:659-662`).
  - After the last entry of a queue page it waits for every put in flight
    (`drain_ios`, `:704`, `:716`) and then removes `entries.size()` entries
    from the queue's front (`:710`, `:722`), with no time check and no
    check that it still holds the lock.
  - If the lock lapses during that wait, another gateway can take it, list
    the same page and free its objects. Both passes then remove a page's
    worth of entries by count (`cls/rgw_gc/cls_rgw_gc.cc:227-375` at both
    tags), so the second removal drops the next page, whose objects nobody
    freed.
- **Impact:** a leak of up to a page of entries' tails, when a pass's puts
  take longer than the lock's remaining time; it never frees live data.
  Freeing a page twice is harmless: each put is keyed by the entry's tag,
  a random string per deletion, and a second put with a retired tag
  changes nothing (`cls/refcount/cls_refcount.cc:88-135` at both tags), as
  does the omap log's removal by tag. Only the queue's removal by count
  reaches the next page.
- **Releases:** v19.2.6, v20.2.4 and main (7ed73efc1be, 2026-09-25,
  unchanged).
- **rgw-go:** meets it as radosgw does: its gc worker takes the same lock
  for the same time, never renews it, and drains and removes as radosgw
  does (`internal/driver/gc.go`), so it carries the same leak risk.
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. The fix would renew the lock before each destructive
  step.
- **Found:** phase 1 unit W, Task 9 review, 2026-10-05, reading
  `RGWGC::process`; derived from the source, not reproduced.

## Tentacle's radosgw never applies rgwx-perm-check-uid, so a user-mode sync pipe's source read goes unchecked

- **Kind:** defect, security-relevant: an authorization bypass (CWE-863)
  that discloses objects across users and tenants (triage estimate CVSS
  about 6.5, 7.7 if scored with a changed scope); a regression in
  Tentacle, unfixed through main. Unreproduced: derived from the source; a
  unit-level model of the logic, no two-zone cluster.
- **Evidence:** paths are under `src/rgw/`, at v20.2.4 unless a line names
  another tag.
  - `LocalEngine::authenticate`, once a system user's signature verifies,
    impersonates the user the request names in `rgwx-perm-check-uid`
    (`rgw_rest_s3.cc:6950-6978`). It reads the parameter with
    `s->info.args.get` (`:6953`; main `:7352-7355` at 6cafff02b39).
  - `RGWHTTPArgs::get` reads only `val_map` (`rgw_common.cc:991-1000`).
    `parse` hands every parameter to `append` (`:904`; the S3 handler parses
    at `rgw_rest_s3.cc:5460-5461`), which files an `rgwx-` name in
    `sys_val_map` instead (`rgw_common.cc:931-937`). Only `set_system()`
    copies `sys_val_map` into `val_map` (`rgw_common.h:462-467`); its one
    caller is `SysReqApplier::modify_request_state`
    (`rgw_auth_filters.h:355`), which `Strategy::apply` runs after the
    engine has granted (`rgw_auth.cc:509`, `:540`). `SysReqApplier` reads
    `rgwx-uid` with `sys_get` instead (`rgw_auth_filters.h:318`).
  - So the parameter always reads empty, and the request runs as the system
    user. `SysReqApplier::is_admin` is true for a system user that is not
    impersonating (`rgw_auth_filters.h:284-290`), so the permission override
    serves it (`rgw_process.cc:228-235`).
  - A destination zone's user-mode sync pipe sends the parameter, naming the
    pipe's user, on the object fetch, together with `rgwx-prepend-metadata`
    (`driver/rados/rgw_data_sync.cc:2994-3044`,
    `driver/rados/rgw_cr_rados.cc:815-817`, `driver/rados/rgw_rados.cc:4589`,
    `rgw_rest_conn.cc:330-335`). The source answers every system GET with
    prepended metadata with `Rgwx-Perm-Checked: true` and keeps the object's
    tags for an admin (`rgw_rest_s3.cc:420-439`; main `:451`).
  - The destination checks the source bucket against the pipe's user only
    when that header is missing (`driver/rados/rgw_rados.cc:3618-3627`,
    `:3837-3852`, `:4504-4528`; main `:4044`). The read check Squid's
    destination made in its sync filter
    (`driver/rados/rgw_data_sync.cc:2854-2859` at v19.2.6) is gone, removed
    by a3f40b4ec6f, "rgw: pass uid on fetch object in data sync"; only the
    write into the destination bucket is still checked
    (`driver/rados/rgw_data_sync.cc:3016`).
  - Introduced by 0e650ea2766, "rgw: SysReqApplier overrides is_admin_of
    based on impersonation", which came to main with
    [ceph/ceph#61962](https://github.com/ceph/ceph/pull/61962); its
    cherry-pick cedcb3773c9 is first in v20.1.0.
  - The fix the evidence points to is one word: read the parameter with
    `args.sys_get` instead of `args.get` (`rgw_rest_s3.cc:6953`), as
    `SysReqApplier` already reads `rgwx-uid`. Other
    `args.get(RGW_SYS_PARAM_PREFIX ...)` reads that run before
    `set_system()` deserve the same audit.
- **Impact:** between a source zone and a destination zone both on Tentacle
  or later, a user-mode sync pipe replicates source objects, and their tags,
  without any check that the pipe's user may read them. S3
  PutBucketReplication creates every pipe in user mode, with the requester's
  owner as its user (`rgw_rest_s3.cc:1404-1405`), and needs only
  s3:PutReplicationConfiguration on the source bucket
  (`rgw_op.cc:1505-1515`). A principal allowed to configure a bucket's
  replication can therefore copy objects it may not read into a bucket it
  can write in another zone. The destination still checks
  s3:ReplicateObject on the destination bucket
  (`driver/rados/rgw_data_sync.cc:3016`), and drops tags only on an explicit
  s3:ReplicateTags Deny there (`:3023`). Against a source that does not send
  the header, such as Squid, which ignores the parameter, the destination
  instead checks s3:ReplicateObject on the source bucket for the pipe's user
  (`driver/rados/rgw_rados.cc:3837-3852`). That is a bucket-level check, not
  the per-object read check Squid's destination made, and a Squid
  destination still makes its own check
  (`driver/rados/rgw_data_sync.cc:2854-2859` at v19.2.6).
- **Releases:** v20.1.0 on, checked at v20.2.4 and main 6cafff02b39; v19.2.6
  has neither the parameter nor the header.
- **rgw-go:** unaffected. Multisite is excluded, so rgw-go never sends the
  parameter, and it serves a system request carrying it as the system user,
  as Tentacle does in effect (`docs/exclusions.md`, "A system request's
  rgwx-uid names its owner, not its user record").
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. rgw-bug-reproduction's prior-art search
  found no report or fix; the regression came only with ceph/ceph#61962
  (above). a3f40b4ec6f's message names
  [#68884](https://tracker.ceph.com/issues/68884). It is unfiled while
  filing is paused.
- **Found:** phase 1 unit A, Task 9 review, 2026-10-04, by code reading; the
  sync consequence while writing this entry, the same day. Not reproduced.

## radosgw grants a signed CORS preflight without comparing its signature

- **Kind:** two parts.
  - The skip itself is a quirk, benign and not found by us: it is
    deliberate, for presigned URLs whose signed headers a browser's
    preflight cannot carry (fe15b52edb5, "rgw/auth: ignoring signatures for
    HTTP OPTIONS calls", first in v19.1.0, which came with
    [ceph/ceph#55458](https://github.com/ceph/ceph/pull/55458) and was
    extended to SigV2 by
    [ceph/ceph#59977](https://github.com/ceph/ceph/pull/59977)). AWS S3
    does not check a preflight's signature either.
  - Charging the forged preflight to the key's user's rate limit is a
    defect, security-relevant and found by us: a targeted denial of
    service, gated on a per-user or global per-user write-op limit (triage
    estimate CVSS 5.9-7.5).

  Unreproduced: derived from the source and a logic-level model.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `LocalEngine::authenticate` grants a request typed `RGW_OP_OPTIONS_CORS`
    without comparing its signature, SigV2 and SigV4 alike
    (`rgw_rest_s3.cc:6354-6360`, `:6925-6931`; main `:7327` at
    6cafff02b39), once the key index finds the key, the user's account loads
    and the key is in the user's record (`:6325-6352`, `:6896-6923`).
  - Before that, a SigV4 request, and on Tentacle a SigV2 one, must name a
    CORS method in `Access-Control-Request-Method` or it is EINVAL
    (`rgw_auth_s3.cc:705-732`, `:1749-1776`).
  - The request then runs as the key's user: its suspended check applies
    (`rgw_process.cc:372`, `:374`), and the ops log records the key's id and
    user (`LocalApplier::write_ops_log_entry`, `rgw_auth.cc:1118-1125`,
    `:1146-1153`). The preflight op checks no permission
    (`RGWOptionsCORS::verify_permission` returns 0, `rgw_op.h:1752`,
    `:1912`).
  - The rate limiter then charges the request to the key's user: it keys on
    `s->user`'s id and applies that user's limit, or the global user limit,
    taking the anonymous limit only for the anonymous user
    (`rate_limit`, `rgw_process.cc:108-132` at both tags, called at `:248`,
    `:251`). It counts only GET and HEAD as reads, so an OPTIONS request is
    charged as a write (`rgw_ratelimit.h:153-154`, `:154-155`).
- **Impact:** anyone who knows an access key id, but not its secret, can send
  a preflight that runs as that key's user. It learns whether the user is
  suspended (403 UserSuspended), and the ops log attributes the request to
  the key. Where per-user rate limits are set, such preflights drain the
  user's write-op budget, and the user's own writes are then refused as
  rate-limited: a denial of service against that user. What the preflight
  reads is no more than an anonymous preflight reads.
- **Releases:** v19.1.0 on; checked at v19.2.6, v20.2.4 and main
  6cafff02b39.
- **rgw-go:** differs: rgw-go serves no CORS preflight in phase 1 and
  verifies a signed OPTIONS request's signature as any other's
  (`docs/exclusions.md`, "A signed OPTIONS request is verified over OPTIONS
  and answered 501 until CORS lands"). Phase 2's CORS work decides whether
  to take the skip.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. The skip is
  [#64308](https://tracker.ceph.com/issues/64308)'s, named by fe15b52edb5's
  message. For the rate-limit charge, rgw-bug-reproduction's prior-art
  search found no report or fix; the fix would exempt `RGW_OP_OPTIONS_CORS`
  from `rate_limit()`. It is unfiled while filing is paused.
- **Found:** phase 1 unit A, Task 9, 2026-10-04, transcribing
  `LocalEngine::authenticate`; confirmed by the Task 9 review. Not
  reproduced.

## radosgw's copy drops its tail references after a head write that timed out

- **Kind:** defect, low (triage estimate CVSS about 2.2 at most), not
  security-relevant, unfixed at v20.2.4; not found by us: it is the timeout edge
  of [#80899](https://tracker.ceph.com/issues/80899). Unreproduced: derived from
  the source.
- **Evidence:** paths are under `src/rgw/driver/rados/`; each pair of
  lines is v19.2.6's, then v20.2.4's.
  - A copy whose source's tails it can share takes a refcount reference on
    each, `cls_refcount_get(tag + '\0', implicit)`, before it writes its
    head (`RGWRados::copy_obj`, `rgw_rados.cc:4906-4952`, `:5166-5212`).
  - When `write_meta` fails, `copy_obj` jumps to `done_ret`
    (`:4983-4986`, `:5243-5246`), which puts every reference whose get
    succeeded (`:4990-5031`, `:5250-5290`), whatever the error.
  - `write_meta` returns -ETIMEDOUT when the head write timed out, and its
    own `done_cancel` then leaves the index entry pending because "rgw
    can't determine whether or not the rados op succeeded"
    (`rgw_rados.cc:3369-3380`, `:3522-3533`). A PUT keeps its tails in
    that case for the same reason (`AtomicObjectProcessor::complete`,
    `rgw_putobj_processor.cc:395-401`, `:429-434`), since commit
    23fcab7fc6b, "rgw: fix data corruption when rados op return
    ETIMEDOUT" (on squid as 63505589868); `copy_obj` was not changed.
  - A timeout does not stop the write: the Objecter cancels the op with
    -ETIMEDOUT and sends the OSD no abort, so the head can still commit.
    So a head write that times out and then lands names the source's
    tails without a reference under its tag. Deleting or overwriting the
    source queues those tails for the GC under the source's tail tag, and
    the GC's `cls_refcount_put(tag, implicit)` (`rgw_gc.cc:684`, `:696`)
    drops the tails' remaining reference, the writer's implicit one, and
    removes them (`cls/refcount/cls_refcount.cc:112-132` at both tags).
- **Impact:** data loss for the copy: once the source goes, a GET of the
  copy's key returns an unreadable object, failing with EIO or ENOENT on
  the missing tail; it never serves wrong bytes. The copy is not merely an
  unlisted orphan: its index entry stays pending, but a GET or HEAD of a
  non-versioned key stats the head object directly
  (`get_obj_state_impl`, `driver/rados/rgw_rados.cc:6144-6150` at v19.2.6,
  `:6898-6904` at v20.2.4), and the index serves only listings and
  versioned lookups. librados reports ETIMEDOUT only with
  `rados_osd_op_timeout` set, which defaults to 0, no timeout
  (`common/options/global.yaml.in:6379-6386` at v19.2.6, `:6538-6545` at
  v20.2.4), so the defect is configuration-gated and no client can steer
  it.
- **Releases:** checked at v19.2.6 and v20.2.4.
- **rgw-go:** differs: it keeps the references after a head write that
  timed out, a possible tail leak and never a premature free: a head that
  never lands leaks them (`shareTails`, `internal/driver/copy.go`;
  `docs/exclusions.md`, "A copy keeps its tail references when its head
  write timed out").
- **Upstream:** the timeout edge of the public
  [#80899](https://tracker.ceph.com/issues/80899), the copy's tail references
  after a failed head write. Its fix,
  [ceph/ceph#72098](https://github.com/ceph/ceph/pull/72098), handles only the
  canceled edge (next entry), not the timeout.
- **Found:** phase 1 unit W, Task 8, 2026-10-05, transcribing
  `copy_obj`; derived from the source, not reproduced.

## radosgw's copy keeps its tail references when its head write loses a race

- **Kind:** defect, not security-relevant, unfixed at v20.2.4, with a fix in
  review; not found by us: it is
  [#80899](https://tracker.ceph.com/issues/80899). Unreproduced here: derived
  from the source; upstream reproduced it.
- **Evidence:** paths are under `src/rgw/driver/rados/`; each pair of
  lines is v19.2.6's, then v20.2.4's.
  - A copy that shares its source's tails takes a reference on each under
    its tag before it writes its head (`rgw_rados.cc:4906-4952`,
    `:5166-5212`).
  - Its head write is unconditional, so a head another writer replaced,
    removed or created between the copy's read and its write fails the
    guarded op with ECANCELED, ENOENT or EEXIST, and `_do_write_meta`
    cancels the index entry, sets `meta.canceled` and returns 0
    (`:3369-3391`, `:3522-3544`).
  - `copy_obj` returns 0 on that (`:4983-4988`, `:5243-5248`) without
    reading `meta.canceled`, so `done_ret` never runs and the references
    stay, though no head names the tails under the copy's tag.
  - Once the source is deleted or overwritten, the GC's put under the
    source's tail tag drops the writer's implicit reference and leaves
    the copy's, so the tails are never removed
    (`cls/refcount/cls_refcount.cc:112-132` at both tags).
- **Impact:** a storage leak of the shared tails for each copy that loses
  such a race; no data is lost or exposed. The leak is the copier's own
  source tails, bounded by its quota.
- **Releases:** checked at v19.2.6 and v20.2.4.
- **rgw-go:** fixed: it answers success as radosgw does, a copy onto itself
  excepted, and rolls the references back when the write was canceled
  (`unrefTails`, from `shareTails`, `internal/driver/copy.go`), as `done_ret`
  would and as ceph/ceph#72098 does (`docs/exclusions.md`, "A copy drops its
  tail references when its head write loses a race").
- **Upstream:** [#80899](https://tracker.ceph.com/issues/80899) (Fix Under
  Review), which upstream's workunit `test_losing_copy` reproduces. The fix in
  review, [ceph/ceph#72098](https://github.com/ceph/ceph/pull/72098), adds
  `if (write_op.meta.canceled) goto done_ret;` to `copy_obj`, and also fixes
  [#80902](https://tracker.ceph.com/issues/80902), so that a canceled write
  reliably means the head was not written.
- **Found:** phase 1 unit W, Task 8, 2026-10-05, transcribing
  `copy_obj`; derived from the source, not reproduced.

## Squid labels a copy to another storage class with its source's class

- **Kind:** defect, not security-relevant, fixed in v20.2.3 and v21.0.0,
  not on squid as of a742f50616e; prior art, fixed upstream before we met
  it. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`.
  - v19.2.6's `copy_obj` keeps the source's `user.rgw.storage_class` in
    the attrs a COPY directive carries over
    (`driver/rados/rgw_rados.cc:4765-4787` at v19.2.6).
  - A copy to another storage class streams its data
    (`:4847-4849`), and its head write sets that attr, then sets
    `user.rgw.storage_class` again from the new manifest's tail class only
    when that is not empty (`_do_write_meta`, `:3220-3262`). A copy into
    a placement's default class, whose rule has an empty class, so keeps
    the source's class, such as COLD, on a head whose data lies in the
    STANDARD pool. A copy of such an object within the default class
    shares its tails (`:4847-4866`) and keeps the label the same way.
  - GetObject and HeadObject report `x-amz-storage-class` from that attr
    (`rgw_rest.cc:106`, `rgw_rest_s3.cc:544-548` at v19.2.6), while the
    bucket index entry carries the manifest's class, so a listing shows
    STANDARD.
  - v20.2.4 erases the attr from the source's attrs
    (`driver/rados/rgw_rados.cc:5028` at v20.2.4), with commit
    fdb78c7fb07, "rgw: implement CopyObject for encrypted object", first
    tagged in v21.0.0, and on tentacle as dbf6b8e4b07, first tagged in
    v20.2.3; v20.2.2 lacks the erase. The squid branch lacks it
    (a742f50616e, 2026-09-03).
- **Impact:** a copied object reports the wrong storage class to
  GetObject and HeadObject; its data is right. Its index entry starts with
  the right class, but a listing that repairs the entry rebuilds it from
  the head's attr (`check_disk_state`, `driver/rados/rgw_rados.cc:10397-10400`,
  `:10433` and `:10447` at v19.2.6; rgw-go's `checkDiskState` does the
  same), after which the listing shows the wrong class too,
  and lifecycle, which reads the index entry, can transition the object by
  it.
- **Releases:** v19.2.6, and Tentacle before v20.2.3. v20.2.4 is not
  affected.
- **rgw-go:** works around it on Squid: it drops the source's attr on
  both releases, as v20.2.4 does, so a copy is labeled with its
  destination placement's class alone (`copyAttrs`,
  `internal/driver/copy.go`; `docs/exclusions.md`, "A copy is labeled
  with its destination's storage class on both releases").
- **Upstream:** fixed as a side effect of
  [#23264](https://tracker.ceph.com/issues/23264), server-side encryption
  for CopyObject, by [ceph/ceph#63794](https://github.com/ceph/ceph/pull/63794)
  and on tentacle by
  [ceph/ceph#69277](https://github.com/ceph/ceph/pull/69277). The issue's
  Backport field names squid, but the squid branch carries no backport.
- **Found:** phase 1 unit W, Task 8, 2026-10-05, comparing `copy_obj` at
  both tags; derived from the source, not reproduced.

## radosgw's copy of an object onto itself can land its old manifest on tails queued for the GC

- **Kind:** defect, unfixed at v20.2.4. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `RGWCopyObj::execute` loads the source's state once
    (`rgw_op.cc:5599`, `:6164`), and `copy_object` hands `copy_obj` the
    destination's own object context (`driver/rados/rgw_sal_rados.cc:2976-2977`,
    `:3819-3820`), so a copy onto itself reads the head once as the source
    and writes it as a destination it has not read.
  - A copy onto itself keeps the source's tails, takes no references, and
    writes the source's manifest back with `keep_tail`
    (`driver/rados/rgw_rados.cc:4955-4957` and `:4965-4983`, `:5215-5243`).
  - `write_meta` writes that head with an exclusive create, and on EEXIST
    invalidates the destination's state, reads the head again and guards
    the write on the write tag it reads then (`:3305-3308` and
    `:3419-3440`, `:3458-3461` and `:3572-3594`), not on the tag the copy
    read.
  - A PUT that overwrites the key between the copy's read and its head
    write queues the old tails for the GC under their tail tag; the copy's
    guarded write then matches the PUT's head and replaces it with the old
    manifest, whose tails it keeps, and the PUT's own tails are queued
    nowhere. A DELETE in the same window queues the tails the same way, and
    the copy's exclusive create then succeeds and brings the head back.
  - The GC later puts each of those tails with the old tail tag and
    `implicit_ref` (`driver/rados/rgw_gc.cc:684`, `:696`), which drops the
    tail writer's implicit reference, the only one, and removes the tail
    (`cls/refcount/cls_refcount.cc:112-132` at both tags).
- **Impact:** data loss: after `rgw_gc_obj_min_wait` the object keeps
  only its head chunk while its manifest names tails that are gone, and a
  PUT the copy overwrote leaks its tails. A client reaches it with a
  self-copy that changes metadata (`x-amz-metadata-directive: REPLACE`)
  racing a PUT or DELETE of the same key.
- **Releases:** checked at v19.2.6 and v20.2.4.
- **rgw-go:** guards a copy onto itself on the head it read, without an
  exclusive create, and answers a lost race with 409
  ConcurrentModification or 404 NoSuchKey, leaving the newer object or the
  deletion (`shareTails`, `internal/driver/copy.go`, and `headWrite.expect`,
  `internal/driver/headwrite.go`; `docs/exclusions.md`, "A copy onto
  itself is guarded on the head it read").
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit W, Task 8 review, 2026-10-05, reading
  `copy_obj`'s self-copy path with `write_meta`; derived from the source,
  not reproduced.

## radosgw fails every tail-sharing copy on a zero rgw_max_copy_obj_concurrent_io

- **Kind:** defect, unfixed at v20.2.4. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/`.
  - `rgw_max_copy_obj_concurrent_io` is an int with no minimum
    (`common/options/rgw.yaml.in:2033-2040` at v19.2.6, `:2133-2140` at
    v20.2.4), and `copy_obj` makes its refcount throttle from it
    (`rgw/driver/rados/rgw_rados.cc:4911` at v19.2.6, `:5171` at v20.2.4).
  - `BlockingAioThrottle::get` and `YieldingAioThrottle::get` answer a
    request whose cost exceeds the window with EDEADLK
    (`rgw/rgw_aio_throttle.cc:40-42` and `:131-132` at both tags), and each
    refcount get costs 1, so with a window of 0 the first get fails,
    `copy_obj` returns EDEADLK, and the copy answers 500 UnknownError, which
    no S3 error names (`rgw_common.cc:346-353` at v19.2.6, `:359-366` at
    v20.2.4).
  - A negative value converts to a window near 2^64 and limits nothing.
- **Impact:** with the option at 0, every copy whose source's tails can be
  shared, the common same-placement copy of an object larger than its
  head, fails; streamed copies and copies of small objects still work.
- **Releases:** checked at v19.2.6 and v20.2.4.
- **rgw-go:** uses a zero as 1 with an error-level log line, as it does a
  zero `rgw_bucket_index_max_aio` (`readWriteOptions`,
  `internal/driver/writer.go`; `docs/exclusions.md`, "Shard counts and the
  bucket-index AIO limit that are not positive"), and keeps a negative
  value, which limits nothing, as radosgw does.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit W, Task 8, 2026-10-05, transcribing
  `copy_obj`'s refcount loop; derived from the source, not reproduced.

## radosgw's bucket creation retry leaks the abandoned instance and repeats its log layout

- **Kind:** defect, unfixed through main: a leak, not security-relevant.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/driver/rados/`.
  - `RGWRados::create_bucket` makes up to 20 tries. Each initializes the
    index of a new bucket id, then writes the instance and the entry point
    exclusively (`rgw_rados.cc:2372-2418` at v19.2.6, `:2477-2526` at
    v20.2.4). When the entry point exists, it reads the bucket by name, and
    when that read finds none, a concurrent delete having removed it, it
    starts the next try at once (`continue`, `rgw_rados.cc:2422-2429` at
    v19.2.6, `:2530-2537` at v20.2.4). That try's instance and index shards
    are written and never removed: only a try that finds another instance
    removes its own (`:2434-2447` at v19.2.6, `:2542-2555` at v20.2.4).
  - Every try works on the same `RGWBucketInfo`, `RadosBucket`'s own
    `info` (`rgw_sal_rados.cc:167-171` at v19.2.6, `:176-181` at v20.2.4),
    and `init_default_bucket_layout` appends the in-index log generation to
    its layout's `logs` without clearing them (`rgw_bucket.cc:2798-2800` at
    v19.2.6, `:2916-2918` at v20.2.4). A bucket created on its nth try is
    stored with n copies of log generation 0, in the live bucket's
    instance.
  - Main at e6dd4bc2a2c is unchanged (`continue` at `rgw_rados.cc:2620-2621`,
    the append at `rgw_bucket.cc:3100-3102`), and its
    `RGWRados::create_vector_bucket` repeats the pattern
    (`rgw_rados.cc:2653-2716`).
- **Impact:** a create that loses to a bucket created and deleted under the
  same name, in the window between its entry-point write and its re-read,
  leaves an instance object in the domain root and a full set of empty index
  shards for each abandoned try, which nothing references or removes. The
  bucket it then creates lists its log generation more than once, which only
  multisite's bucket sync and log trimming read; that path was not traced.
  The 20 tries bound both.
- **Releases:** v19.2.6, v20.2.4 and main e6dd4bc2a2c.
- **rgw-go:** removes an abandoned try's instance and index before the next
  try and gives each try a fresh layout: it builds a new bucket info on
  every try (`Store.CreateBucket` in `internal/driver/bucketops.go`;
  `docs/exclusions.md`, "An abandoned bucket-creation try is removed").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix.
- **Found:** phase 1 unit M, Task 7, 2026-10-05, transcribing
  `create_bucket` at both tags; derived from the source, not reproduced.

## radosgw reports a bucket whose owner link failed as created

- **Kind:** defect, unfixed through main, not security-relevant.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`.
  - Once a new bucket's instance and entry point are written,
    `RadosBucket::create` adds it to its owner's bucket list. When that
    link fails, it unlinks the bucket and assigns the unlink's result to the
    value it returns (`ret = unlink(dpp, params.owner, y)`,
    `driver/rados/rgw_sal_rados.cc:190-197` at v19.2.6, `:207-214` at
    v20.2.4, `:234-241` on main e6dd4bc2a2c), so an unlink that succeeds
    turns the link's failure into 0. The error is lost there, not in
    `RGWCreateBucket::execute`.
  - The rollback stops at the unlink: nothing removes the instance or the
    entry point the create wrote.
  - `RGWCreateBucket::execute` then finishes without an error
    (`rgw_op.cc:3640-3647` at v19.2.6, `:3868-3875` at v20.2.4), and
    `send_response` answers 200 (`rgw_rest_s3.cc:2544-2555` at v19.2.6,
    `:2705-2716` at v20.2.4).
- **Impact:** the client is told its bucket was created. The bucket exists,
  entry point and instance, but is in no owner's list: ListBuckets does not
  show it, the owner's stats do not count it, and `max_buckets` does not
  count it either. The owner's re-create repairs it, as a partial creation
  (`rgw_sal_rados.cc:173-190` at v19.2.6). It is no squat: another user's
  create of the name answers BucketAlreadyExists, as for any bucket.
- **Releases:** v19.2.6, v20.2.4 and main e6dd4bc2a2c.
- **rgw-go:** returns the link's failure after the same unlink, and, as
  radosgw does, leaves the instance and entry point for the owner's
  re-create (`Store.CreateBucket` in `internal/driver/bucketops.go`;
  `docs/exclusions.md`, "A failed owner link fails the create").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. The nearest is the inverse defect,
  [#69738](https://tracker.ceph.com/issues/69738), an owner's list still
  naming a bucket that a delete racing its creation removed, which
  [ceph/ceph#61684](https://github.com/ceph/ceph/pull/61684) fixed on the
  create side (first in v20.1.0).
- **Found:** phase 1 unit M, Task 7, 2026-10-05, transcribing
  `RadosBucket::create` at both tags; derived from the source, not
  reproduced.

## radosgw's bucket delete answers success when its instance removal loses a race

- **Kind:** quirk: the success is deliberate upstream, not found by us and
  not security-relevant, but it costs consistency, as below. Unreproduced:
  derived from the source.
- **Evidence:** paths are under `src/rgw/`.
  - `RadosBucket::remove` reloads the bucket, which leaves the instance's
    version in `info.objv_tracker` (`driver/rados/rgw_sal_rados.cc:357` and
    `:610-637` at v19.2.6, `:374` at v20.2.4;
    `services/svc_bucket_sobj.cc:343-371` at v19.2.6, `:293-320` at
    v20.2.4).
  - `RGWRados::delete_bucket` removes the entry point, then the instance
    under that tracker, and returns the instance removal's error, -ECANCELED
    when the instance changed since the reload (`driver/rados/rgw_rados.cc:5279-5293`
    at v19.2.6, `:5993-6006` at v20.2.4; `driver/rados/rgw_bucket.cc:3164-3181`
    at v19.2.6, `:3295-3310` at v20.2.4; only -ENOENT is tolerated,
    `services/svc_bucket_sobj.cc:577-580` at v19.2.6, `:516-519` at
    v20.2.4). `remove` returns that error before it cleans the index and
    unlinks the bucket (`rgw_sal_rados.cc:445-450` at v19.2.6, `:462-467`
    at v20.2.4).
  - `RGWDeleteBucket::execute` turns -ECANCELED into success on purpose:
    its comment reads "lost a race, either with mdlog sync or another
    delete bucket operation. in either case, we've already called
    ctl.bucket->unlink_bucket()" (`rgw_op.cc:3792-3797` at v19.2.6,
    `:4001-4006` at v20.2.4, `:4470-4475` on main e6dd4bc2a2c; since
    e9cf41f9bfd, "rgw: fix for bucket delete racing with mdlog sync",
    2016). On this path `remove` has returned before its own unlink, so
    the unlink the comment counts on is the race winner's.
- **Impact:** against another DELETE the success is right. A DELETE that
  races any other write of the bucket's instance, such as a PutBucketAcl,
  PutBucketTagging, a reshard or a metadata sync, after `remove`'s reload
  is answered 204 while only the entry point is gone. The instance and the
  index stay behind, unreferenced, and the bucket stays in its owner's list,
  so ListBuckets shows a bucket that every request answers NoSuchBucket for,
  and `max_buckets` counts it: the ghost bucket of
  [#69738](https://tracker.ceph.com/issues/69738).
- **Releases:** v19.2.6, v20.2.4 and main e6dd4bc2a2c.
- **rgw-go:** reads the instance again when the delete begins, as
  `remove` does, removes it under the version read, and when another write
  changed it since, reads it again from RADOS and retries, so the delete
  goes on to the index the last read names and the owner's entry. After
  twenty lost removals it answers 500 InternalError, leaving what radosgw's
  204 leaves
  (`Store.DeleteBucket` in `internal/driver/bucketops.go`;
  `docs/exclusions.md`, "A bucket delete retries an instance removal that
  lost to another write").
- **Upstream:** prior art: [#69738](https://tracker.ceph.com/issues/69738)
  (Resolved) reported the ghost bucket, and
  [ceph/ceph#61684](https://github.com/ceph/ceph/pull/61684) fixed its
  create side, leaving this delete-side mapping as it was. Nothing to file
  for the mapping itself.
- **Found:** phase 1 unit M, Task 7, 2026-10-05, transcribing
  `delete_bucket` at both tags; derived from the source, not reproduced.

## radosgw's bucket delete unlinks a bucket re-created under the same name from its owner

- **Kind:** defect, unfixed through main: consistency, not
  security-relevant. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`.
  - `RGWRados::delete_bucket` reads the entry point afresh and, when it
    names another instance, leaves it in place
    (`rgw/driver/rados/rgw_rados.cc:5254-5277` at v19.2.6, `:5968-5991` at
    v20.2.4).
  - `RadosBucket::remove` then unlinks the bucket from its owner whatever
    the entry point named (`rgw/driver/rados/rgw_sal_rados.cc:461-462` at
    v19.2.6, `:482-483` at v20.2.4), through `RGWBucketCtl::unlink_bucket`
    and `rgwrados::buckets::remove` (`rgw/driver/rados/rgw_bucket.cc:3407-3432`
    and `rgw/driver/rados/buckets.cc:62-78` at v19.2.6;
    `rgw_bucket.cc:3506-3518` and `buckets.cc:62-78` at v20.2.4).
  - `cls_user_remove_bucket` keys the owner's entry by the bucket's name
    alone (`get_key_by_bucket_name`, `cls/user/cls_user.cc:45-48`) and
    removes whatever instance it names (`:239-276`), though its op carries
    the bucket's marker and id (`cls_user_bucket`,
    `cls/user/cls_user_types.h:15-18`); all at both tags.
- **Impact:** when a DELETE reads a bucket, another DELETE of the same
  bucket completes, and the owner creates the bucket again before the first
  DELETE re-reads the entry point, the first DELETE removes the new bucket's
  entry from its owner's list. The new bucket exists and answers by name,
  but ListBuckets does not show it, and the owner's stats, quota and
  `max_buckets` do not count it until the owner re-creates it again. Stats
  sync does not restore the entry: `cls_user_set_buckets_info` skips a
  bucket it does not find (`cls/user/cls_user.cc:151-153`). It is the
  delete-side mirror of
  [#69738](https://tracker.ceph.com/issues/69738).
- **Releases:** v19.2.6, v20.2.4 and main e6dd4bc2a2c, whose
  `cls_user_remove_bucket` is the same (`cls/user/cls_user.cc:239-276`).
- **rgw-go:** removes the owner's entry only while it names the instance
  being deleted: it compares the entry's decoded bucket id, and removes it
  under a comparison with the entry it read, retrying a lost comparison up
  to five times (`Store.unlinkInstance` in `internal/driver/bucketops.go`;
  `docs/exclusions.md`, "An owner's entry naming another instance stays").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix; [ceph/ceph#61684](https://github.com/ceph/ceph/pull/61684)
  fixed only the create side of
  [#69738](https://tracker.ceph.com/issues/69738). The fix would remove the
  entry only when its bucket id and marker match the op's.
- **Found:** phase 1 unit M, Task 7 review, 2026-10-05, transcribing
  `RadosBucket::remove` at both tags; derived from the source, not
  reproduced.

## Tentacle cannot delete an indexless bucket it created

- **Kind:** defect, unfixed through main: a correctness defect under
  version skew, not security-relevant. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/`.
  - Squid's `init_index` and `clean_index` create and remove the shard
    objects whatever the index type (`rgw/services/svc_bi_rados.cc:354-393`
    at v19.2.6). Tentacle's create and remove none for an index that is not
    Normal (`:456-458` and `:521-523` at v20.2.4). The guards came with
    c7774831d5c, "rgw/rados: indexless buckets skip init_index/clean_index",
    merged by [ceph/ceph#61269](https://github.com/ceph/ceph/pull/61269)
    and first in v20.0.0, which left `check_bucket_empty` unguarded
    (`rgw/driver/rados/rgw_rados.cc:5723` at v20.2.4, `:6253` on main
    e6dd4bc2a2c).
  - `RGWDeleteBucket::execute` checks an own bucket's emptiness whatever
    its index type (`rgw/rgw_op.cc:3761-3772` at v19.2.6, `:3968-3981` at
    v20.2.4), through `RadosBucket::check_empty` and
    `RGWRados::check_bucket_empty`, which lists the shards with
    `cls_bucket_list_unordered` (`rgw/driver/rados/rgw_sal_rados.cc:777-780`
    and `rgw/driver/rados/rgw_rados.cc:5741-5755` at v20.2.4). That opens
    the shard objects without looking at the index type
    (`rgw/services/svc_bi_rados.cc:191-215` at v19.2.6, `:199-216` at
    v20.2.4).
  - `bucket_list` is a read method (`cls/rgw/cls_rgw.cc:4693` at v19.2.6,
    `:5110` at v20.2.4), so on a missing shard object the OSD answers
    -ENOENT, which the DELETE returns: 404 NoSuchKey
    (`rgw/rgw_common.cc:98` at v20.2.4).
- **Impact:** on Tentacle, an indexless bucket created there can never be
  deleted through S3: every DELETE answers 404 NoSuchKey and the bucket
  stays. An indexless bucket a Squid gateway created has shard objects, so
  a Tentacle DELETE passes the check, but Tentacle's `clean_index` then
  skips them, and they stay in the index pool, unreferenced.
- **Releases:** v20.2.4 and main e6dd4bc2a2c. v19.2.6 is not affected for
  buckets it creates.
- **rgw-go:** reproduces both halves, as radosgw does at each release: on
  Tentacle the emptiness check fails on the missing shards with NoSuchKey
  and the bucket stays, and an index that is not Normal is not cleaned, so
  a Squid-made indexless bucket's shard objects stay behind
  (`Store.checkBucketEmpty`, `Store.cleanIndex` and
  `Store.hasIndexObjects` in `internal/driver/bucketops.go`, with a spec
  in `bucketops_test.go`; `docs/exclusions.md`, "An indexless bucket is
  deleted as each release deletes it"). Skipping the check could orphan
  objects.
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix; the closest,
  [#73861](https://tracker.ceph.com/issues/73861) and
  [ceph/ceph#69533](https://github.com/ceph/ceph/pull/69533), concern
  another path. The fix would guard `check_bucket_empty` as the other two
  are guarded.
- **Found:** phase 1 unit M, Task 7 review, 2026-10-05, tracing the delete
  of an indexless bucket at both tags; derived from the source, not
  reproduced.

## radosgw takes a SigV4 request without x-amz-content-sha256 as UNSIGNED-PAYLOAD

- **Kind:** quirk.
- **Evidence:** paths are under `src/rgw/`.
  - `get_v4_exp_payload_hash` reads `x-amz-content-sha256` and, when the
    header is absent, returns `UNSIGNED-PAYLOAD` (`rgw_auth_s3.h:638-661` at
    v19.2.6, `:641-664` at v20.2.4, the body unchanged).
  - Its comment says a client must send the header and allows the literal
    only for query-string authentication, but `get_auth_data_v4` calls it
    for every S3 op on either route, header-signed requests included
    (`rgw_rest_s3.cc:5817` at v19.2.6, `:6384` at v20.2.4), so a header-signed request without the header is
    verified over `UNSIGNED-PAYLOAD` and its body is not hashed. AWS refuses
    such a request.
- **Releases:** v19.2.6 and v20.2.4, the tags checked; the same fallback is
  on main e6dd4bc2a2c (`rgw_auth_s3.h:660`).
- **rgw-go:** mirrors it (`expectedPayloadHash`, `internal/auth/sigv4.go`).
  It is a hard requirement: go-ceph's rgw/admin client, which Rook runs,
  signs with the unsigned-payload hash and never sends the header
  (`docs/exclusions.md`, "Signature quirks are radosgw's, not AWS's").
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit A's plan, transcribing `get_v4_exp_payload_hash`;
  registered in unit A, Task 10, 2026-10-05. Derived from the source, not
  reproduced.

## rgw_s3_auth_disable_signature_url denies header-signed requests too

- **Kind:** defect, unfixed through main: an over-denial gated on the
  operator's configuration, which fails closed, not security-relevant.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`.
  - The option is documented as presigned-only: "If enabled, any request
    that is presigned with either V2 or V4 signature will be denied"
    (`common/options/rgw.yaml.in:911-916` at v19.2.6, `:970-975` at
    v20.2.4).
  - `AWSGeneralAbstractor::get_auth_data` throws
    `ERR_PRESIGNED_URL_DISABLED` whenever the option is on, after
    `discover_aws_flavour` and before it looks at the version or the route
    it found (`rgw/rgw_rest_s3.cc:5607-5610` at v19.2.6, `:6163-6166` at
    v20.2.4, `:6544-6547` on main e6dd4bc2a2c). Every SigV2 and SigV4
    request reaches it, header-signed ones included; only anonymous
    requests, which the anonymous engine takes first, do not.
  - `Strategy::apply` turns the error into EPERM with the message
    "Presigned URLs are disabled by admin" (`rgw/rgw_auth.cc:505-509` at
    v19.2.6, `:520-524` at v20.2.4), so every signed request is 403
    AccessDenied.
- **Impact:** an operator who turns the option on to refuse presigned URLs
  refuses every authenticated request, and only anonymous access remains.
- **Releases:** every release with the option, which came to Squid with
  2d6efcc6623 (ceph/ceph#56343, first in v19.1.0); checked at v19.2.6,
  v20.2.4 and main e6dd4bc2a2c.
- **rgw-go:** mirrors it, so a cluster that sets the option behaves the
  same with either gateway (`Verifier.Authenticate`,
  `internal/auth/verifier.go`).
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. The option came with
  [#64797](https://tracker.ceph.com/issues/64797), which its commit
  message names, through
  [ceph/ceph#56044](https://github.com/ceph/ceph/pull/56044) on main and
  ceph/ceph#56343 on squid. The fix would check that the route is the
  query string before refusing.
- **Found:** phase 1 unit A's plan, transcribing `get_auth_data`;
  registered in unit A, Task 10, 2026-10-05. Not reproduced.

## radosgw keeps repeated SigV4 query keys in arrival order

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source. Not a quirk: the function names AWS's canonical-request step 3
  as its model (`rgw_auth_s3.cc:618-619` at v19.2.6, `:624-625` at
  v20.2.4), and that step sorts by value too, so the arrival order is a
  gap in the transcription, not a choice.
- **Evidence:** paths are under `src/rgw/`.
  - `get_v4_canonical_qs` inserts each query pair into a
    `std::multimap<std::string, std::string>` and joins the map in order
    (`rgw_auth_s3.cc:620-659` at v19.2.6, `:626-665` at v20.2.4). A
    multimap orders by key only, and an insert goes after the elements
    with an equal key, so repeated keys keep the order they arrived in.
  - AWS sorts the canonical query string by key and then by value. A
    client that sends `a=2&a=1` signs `a=1&a=2`, and radosgw computes
    `a=2&a=1`, so the signature does not match.
- **Impact:** a correctly signing AWS client that sends a repeated query
  key with its values out of order gets 403 SignatureDoesNotMatch. A
  request signed in arrival order, which AWS would refuse, is accepted.
- **Releases:** v19.2.6 and v20.2.4, the tags checked; the same multimap is
  on main e6dd4bc2a2c (`rgw_auth_s3.cc:645`).
- **rgw-go:** mirrors it: `canonicalQueryV4` sorts the pairs stably by key
  alone (`internal/auth/sigv4.go`), so a request radosgw verifies, rgw-go
  verifies.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit A's plan, transcribing `get_v4_canonical_qs`;
  registered in unit A, Task 10, 2026-10-05. Derived from the source, not
  reproduced.

## radosgw never checks a SigV4 credential scope's date, region or service

- **Kind:** quirk.
- **Evidence:** paths are under `src/rgw/`.
  - `parse_cred_scope` splits the scope into date, region and service, and
    its only caller, `get_v4_signing_key`, feeds them to the signing-key
    HMACs (`rgw_auth_s3.cc:962-1033` at v19.2.6, `:939-1010` at v20.2.4).
    The scope also enters the string to sign whole, and nothing compares
    its fields with the endpoint or with `x-amz-date`.
  - AWS refuses a scope whose region or service is not the endpoint's, or
    whose date is not the date of `x-amz-date`. radosgw verifies a
    signature computed for any region, service or date against the same
    secret.
- **Impact:** the unchecked date undoes AWS's day-scoping of derived
  signing keys: a signing key derived for one date, without the secret,
  signs requests with any `x-amz-date`, bounded only by radosgw's own
  checks on `x-amz-date` (the time skew, or a presigned URL's expiry). The
  unchecked region and service only let a client name any region or
  service.
- **Releases:** v19.2.6 and v20.2.4, the tags checked; `parse_cred_scope`
  is unchanged on main e6dd4bc2a2c (`rgw_auth_s3.cc:983`).
- **rgw-go:** mirrors it: `parseCredScope` and `signingKeyV4`
  (`internal/auth/sigv4.go`) use the scope's fields for the key alone, so a
  signature computed for any region verifies against the same secret.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit A's plan, transcribing `parse_cred_scope`;
  registered in unit A, Task 10, 2026-10-05. Derived from the source, not
  reproduced.

## radosgw's ListMultipartUploads drops its common prefixes and the uploads under them

- **Kind:** defect, unfixed through main: an under-listing, not
  security-relevant. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`.
  - `RadosBucket::list_multiparts` lists the multipart namespace with
    `MultipartMetaFilter` as the listing's `access_list_filter`
    (`driver/rados/rgw_sal_rados.cc:927-933` at v19.2.6, `:947-953` at
    v20.2.4). The filter accepts a name longer than `.meta` that ends in
    it with a dot before (`services/svc_tier_rados.cc:10-32` at both
    tags).
  - With a delimiter, cls_rgw's `bucket_list` returns one entry flagged
    as a common prefix for every run of names holding the delimiter after
    the prefix, named up to and including the delimiter
    (`../cls/rgw/cls_rgw.cc:625-663` at v19.2.6, `:675-713` at v20.2.4).
  - `list_objects_ordered` runs the filter on every entry it keeps after
    the namespace check, before the prefix and the delimiter
    (`driver/rados/rgw_rados.cc:2003-2009` at v19.2.6, `:2106-2112` at
    v20.2.4). A common-prefix entry's name ends in the delimiter, so the
    filter refuses it and the code that would record it as a common prefix
    (`:2019-2087` at v19.2.6, `:2122-2190` at v20.2.4) never runs. Only a
    delimiter that is a suffix of `.meta`, such as `a`, or ends in `.meta`
    can form a prefix whose name passes, such as a whole meta name
    `<key>.<id>.meta`, and that one is recorded (`:2063` and `:2076` at
    v19.2.6, `:2166` and `:2179` at v20.2.4).
  - Main at e6dd4bc2a2c is unchanged: the filter at `rgw_rados.cc:2194-2200`
    runs before the prefix and delimiter checks from `:2202` on, and
    `list_multiparts` sets it at `rgw_sal_rados.cc:1179-1183`.
- **Impact:** ListMultipartUploads with any other delimiter returns no
  CommonPrefixes, and every upload whose meta name holds the delimiter after
  the prefix is missing from the response, with nothing pointing to it: with
  `delimiter=/`, every upload of a key holding a `/` past the prefix. A
  client that finds its incomplete uploads this way, to abort them or to
  resume one, never sees those, and the space they hold stays out of its
  view. With such a delimiter, the common prefixes that survive count toward
  the page, but `RGWListBucketMultiparts::execute` takes the next markers
  from the last upload only (`rgw/rgw_op.cc:6741-6744` at v19.2.6,
  `:7680-7683` at v20.2.4), so a truncated page that ends on prefixes makes
  the next page repeat them, or start over when the page holds no upload.
- **Releases:** v19.2.6, v20.2.4 and main e6dd4bc2a2c.
- **rgw-go:** does not reproduce it. Its listing applies the filter only to
  a name it would list as an upload, so the common prefixes are listed,
  and a truncated page takes its next markers from its last counted item:
  an upload's key and id, or a common prefix as the key marker with no
  upload id marker (`op.UploadListing` and `op.UploadsFromListing` in
  `internal/op/multipart.go`; `docs/exclusions.md`, "ListMultipartUploads
  keeps the common prefixes radosgw drops").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix for the drop;
  [#47527](https://tracker.ceph.com/issues/47527) and its fix,
  [ceph/ceph#43779](https://github.com/ceph/ceph/pull/43779), named the
  common prefixes in the response's XML, a different layer. The fix would
  run the filter after the rollup, or exempt the common-prefix entries,
  and is shared with the next entry's.
- **Found:** phase 1 unit P, Task 5, 2026-10-05, placing the listing's name
  filter at both tags; derived from the source, not reproduced.

## radosgw's ListMultipartUploads applies the prefix and the delimiter to the meta object's name

- **Kind:** defect, unfixed through main, not security-relevant: the
  over-listing stays within one bucket that the requester may list.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`.
  - `RadosBucket::list_multiparts` passes the request's prefix and
    delimiter to the listing of the multipart namespace
    (`rgw/driver/rados/rgw_sal_rados.cc:927-933` at v19.2.6, `:947-953` at
    v20.2.4), whose names are the meta objects', `<key>.<upload id>.meta`
    (`RGWMPObj::init`, `rgw/services/svc_tier_rados.h:53` at both tags).
    The listing matches the prefix against that name and searches the
    delimiter in it past the prefix (`rgw/driver/rados/rgw_rados.cc:2011-2021`
    at v19.2.6, `:2114-2124` at v20.2.4; `cls/rgw/cls_rgw.cc:626` at
    v19.2.6, `:676` at v20.2.4).
  - The key marker becomes `RGWMPObj(key_marker, upload_id_marker)`'s meta
    name (`rgw/rgw_rest.cc:1646-1655` at v19.2.6, `:1651-1660` at v20.2.4),
    `<key>..meta` when no upload id marker is given.
  - An upload id is `2~` and 31 characters of `gen_rand_alphanumeric`
    (`rgw/driver/rados/rgw_sal_rados.cc:3281` at v19.2.6, `:4125` at
    v20.2.4), whose table holds `-` and `_` (`common/random_string.cc:48`
    at both tags).
  - Main at e6dd4bc2a2c passes them the same way
    (`rgw/driver/rados/rgw_sal_rados.cc:1179-1183`).
- **Impact:** S3 applies these parameters to the key, and orders uploads by
  key, then by initiation time. In radosgw:
  - a delimiter found only in `.<upload id>.meta` rolls up an upload whose
    key does not hold it: `.` rolls up every upload, `-` or `_` a random
    share of them, by their ids, and `/` every upload whose id has the
    legacy prefix `2/` (`MULTIPART_UPLOAD_ID_PREFIX_LEGACY`,
    `rgw/rgw_multi.h:15` at both tags). Under "radosgw's
    ListMultipartUploads drops its common prefixes and the uploads under
    them" those uploads then vanish from the response;
  - a prefix that runs past the key, such as `photo.`, matches the uploads
    of the key `photo`;
  - uploads list in the byte order of their meta names, so the key `a-b`
    lists before `a`, and one key's uploads by upload id;
  - a key marker without an upload id marker lists that key's own uploads
    too, where S3 lists only the keys after it.
- **Releases:** v19.2.6, v20.2.4 and main e6dd4bc2a2c.
- **rgw-go:** reproduces it: the prefix, the delimiter, the order and the
  markers apply to the meta names (`op.UploadListing` and
  `op.UploadsFromListing` in `internal/op/multipart.go`). An upload a
  delimiter rolls up stays reachable through its common prefix, which
  rgw-go lists.
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. It shares its root with the previous entry, and so its
  fix: compare the prefix, the delimiter and the markers with the key
  recovered from the meta name, and start a key marker's listing after
  that key.
- **Found:** phase 1 unit P, Task 5, 2026-10-05, transcribing
  `list_multiparts` at both tags; derived from the source, not reproduced.

## Tentacle's DeleteObjects asks for MFA for the keys that need none and not for the versions that do

- **Kind:** defect, security-relevant, high: a version-id delete skips MFA
  delete (triage estimate CVSS about 8.1). A regression, fixed on main,
  not on the release branches; not found by us. Unreproduced: derived from
  the source.
- **Evidence:** paths are under `src/rgw/`.
  - On a bucket with MFA delete enabled, `RGWDeleteMultiObj::execute` asks
    for a verified MFA token when the request names a version. v19.2.6
    tests each key's instance for being set (`rgw_op.cc:7055-7068`);
    v20.2.4 tests it for being empty (`rgw_op.cc:7969-7983`), so it asks
    for MFA when a key names no version and not when every key names one.
  - The inversion came with f28f0d9147d, "RGW | fix conditional Delete and
    MultiDelete" (cherry-picked from 55f5b762c67), which moved the check to
    `RGWMultiDelObject::get_version_id`. v20.2.0 still tests
    `!i.instance.empty()`; v20.2.1 through v20.2.4 carry the inversion.
  - Squid's backport of conditional delete,
    [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932) (merged
    a742f50616e, after v19.2.6), carries it too, as ce98cebb815: the squid
    branch tests `instance.empty()` (`rgw_op.cc:7071` at a742f50616e). No
    v19.2.x tag contains ce98cebb815.
  - b703ec465f3, "rgw: fix inverted MFA check in DeleteMultiObj", restores
    the test on main. The tentacle branch (7411a080411, 2026-09-30) and the
    squid branch (a742f50616e) still carry the inversion.
  - DeleteObject's own check is unaffected (`rgw_op.cc:5548-5553` at
    v20.2.4).
- **Impact:** on an affected gateway, a DeleteObjects on a bucket with MFA
  delete whose every key names a version deletes those versions without
  MFA, which MFA delete exists to prevent; one that names a key without a
  version is refused unless MFA verified, which S3 does not ask for.
- **Releases:** only Tentacle's v20.2.1 through v20.2.4 (f28f0d9147d is
  first in v20.2.1), checked at v20.2.0 and v20.2.4; the squid branch after
  v19.2.6, in no release yet. v19.2.6 is not affected.
- **rgw-go:** checks as v19.2.6 and main do, on both releases, so on
  Tentacle it uses main's corrected check. It verifies no MFA token, so it
  refuses every DeleteObjects naming a version on such a bucket
  (`internal/op/deleteobjects.go`; `docs/exclusions.md`, "Deletes that
  need MFA are refused").
- **Upstream:** [#77869](https://tracker.ceph.com/issues/77869), fixed on
  main by [ceph/ceph#69821](https://github.com/ceph/ceph/pull/69821)
  (merge 8131835af85). The backport tracker,
  [#78708](https://tracker.ceph.com/issues/78708), under
  [#78707](https://tracker.ceph.com/issues/78707), is New with no PR, as
  rgw-bug-reproduction's triage found it.
- **Found:** phase 1 unit W, Task 10, 2026-10-05, reading
  `RGWDeleteMultiObj::execute` at both tags; derived from the source, not
  reproduced.

## radosgw's parse_date writes a NUL one byte past its format buffer

- **Kind:** defect, security-relevant, low: an out-of-bounds stack write
  of a fixed NUL at a fixed offset, which a requester cannot steer (triage
  estimate CVSS about 3.7; up to 7.5 only if a build is shown to crash its
  worker reliably). Unreproduced: derived from the source; an
  AddressSanitizer model reports it.
- **Evidence:** paths are under `src/`.
  - `utime_t::parse_date` (`include/utime.h:397-502` at v19.2.6 and
    v20.2.4, unchanged between them; `include/utime.cc:163` on main
    7ed73efc1be, the buffer at `:179-203`) reads the time after a date's ' '
    or 'T' with a strptime format built from the text itself in
    `char fmt[32] = {0}`: `strncpy(fmt, p, sizeof(fmt) - 1)`, then `%H:%M`
    and `%S` written over its first eight bytes (`include/utime.h:413-437`).
    When the ninth byte is '.', `q` walks the digits after it, from
    `fmt + 9`, and a '+' or '-' where they end becomes `%z` through
    `*q = '%'; *(q+1) = 'z'; *(q+2) = 0;`.
  - A time text of "HH:MM:SS." and 21 digits then a sign puts the sign at
    `fmt[30]`, so `*(q+2) = 0` writes `fmt[32]`, one byte past the array
    (`include/utime.h:436` at both tags), before strptime runs, so no zone
    digits are needed. A shorter digit run
    stays inside it; `fmt[31]` is always the NUL strncpy left, so no longer
    run reaches a sign.
- **Trigger:** a time with a `.`, 21 digits and a sign, such as
  `2026-01-01T01:02:03.012345678901234567890+`. Request text reaches
  `parse_date` through:
  - the admin API's date arguments, `RESTArgs::get_epoch` and `get_time`
    (`rgw/rgw_rest.cc:973` and `:995` at v19.2.6, `:978` and `:1000` at
    v20.2.4);
  - the S3 usage request's dates (`rgw/rgw_op.cc:2618-2626` at v19.2.6,
    `:2850-2858` at v20.2.4);
  - DeleteObject's `x-amz-delete-if-unmodified-since`
    (`RGWDeleteObj_ObjStore_S3::get_params`, `rgw/rgw_rest_s3.cc:3435-3444`
    at v19.2.6, `:3696-3705` at v20.2.4), which
    `RGWDeleteObj::init_processing` reads before any permission check
    (`rgw/rgw_op.cc:5129-5136` at v19.2.6, `:5519` at v20.2.4;
    `init_processing` at `rgw/rgw_process.cc:202`, `verify_permission` at
    `:225`, at both tags), so any requester who can send a DeleteObject
    reaches it, an anonymous one included;
  - every XML date the request documents decode (`rgw/rgw_xml.cc:421-445`
    at both tags);
  - the JSON decoders of a `utime_t` or `real_time`
    (`common/ceph_json.cc:473-512` at both tags), which `PUT
    /admin/metadata` reaches through the document's "mtime"
    (`rgw/rgw_metadata.cc:530` at v19.2.6, `:325` at v20.2.4);
  - `utime_t::parse` (`include/utime.h:504-512` at both tags), with its
    callers' input.
- **Impact:** one zero byte written past a stack array. What it overwrites
  depends on the compiler's frame layout. When it lands on the stack
  protector's canary it most likely changes nothing: glibc's canary keeps
  its lowest byte zero, which is the byte a little-endian write past the
  array reaches first. Otherwise it zeroes one byte of padding, or of
  another local of `parse_date`, such as `tm` or the low byte of the
  `subsec` pointer, whose effect is a wrong parse or, for the pointer, a
  read through a corrupted address. On a build whose canary has a nonzero
  byte there, the stack protector aborts radosgw when `parse_date`
  returns, once per such request. The byte and its offset are fixed, so a
  requester cannot steer the write. AddressSanitizer reports it in a model
  of the function. Not observed on a running gateway.
- **Releases:** every release checked: v19.2.6, v20.2.4 and main
  7ed73efc1be.
- **rgw-go:** not affected. Both of its ports read such a value as the
  format radosgw builds would read it, with the terminator kept inside the
  format: `parseDate` in `internal/admin/request.go`, over
  `internal/strptime`, for the admin API's dates, and `parseDate` in
  `internal/op/deleteobject.go` for DeleteObject's. Each refuses some `%`
  conversions of the value that radosgw's format would run
  (`docs/exclusions.md`, "A date whose time names a conversion rgw-go does
  not run is refused").
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-07; awaiting response. rgw-bug-reproduction's prior-art search
  found no report, and no fix on any branch. The fix would size the format
  buffer for the `%z` it appends, or bound the digit walk.
- **Found:** phase 1 unit W, Task 10, 2026-10-05, porting `parse_date`;
  derived from the source, not reproduced.
- **Found:** phase 1 unit N, Task 3, 2026-10-05, transcribing `parse_date`
  for the admin API's date arguments; derived from the source, not
  reproduced.

## radosgw lets a public ACL through a block of public ACLs on PutObject's grant headers, CopyObject and CreateMultipartUpload

- **Kind:** defect, security-relevant, medium: a bypass of a bucket's
  BlockPublicAcls (triage estimate CVSS 5.3-6.6). Unreproduced end to end:
  derived from the source.
- **Evidence:** paths are under `src/rgw/`; lines are v19.2.6's unless
  marked.
  - A bucket's BlockPublicAcls is checked in two places only
    (`block_public_acls`, `rgw_op.cc:3904` and `:5906`; v20.2.4 `:4113` and
    `:6552`): RGWPutObj's `init_processing` and RGWPutACLs' `execute`.
  - RGWPutObj's check refuses the canned ACLs `public-read`,
    `public-read-write` and `authenticated-read` alone
    (`rgw_op.cc:3903-3909`; v20.2.4 `:4112-4118`). A request that sets its
    ACL with `x-amz-grant-*` headers carries no canned ACL, and
    `create_s3_policy` builds its policy from the headers
    (`rgw_rest_s3.cc:2408-2422` and `:2618`), so a grant of READ to the
    AllUsers group passes the check and the object is stored public.
    RGWPutACLs, by contrast, refuses any policy `is_public` finds
    (`rgw_op.cc:5905-5910`).
  - RGWCopyObj derives from `RGWOp` (`rgw_op.h:1497`; v20.2.4 `:1602`), not
    from RGWPutObj, and checks no block: a copy with `x-amz-acl:
    public-read` or a public grant header stores a public object in a
    bucket that blocks public ACLs.
  - RGWInitMultipart derives from `RGWOp` too (`rgw_op.h:1842`; v20.2.4
    `:2007`) and checks no block, though it builds the upload's policy
    with `create_s3_policy` from the same headers
    (`rgw_rest_s3.cc:3969-3979`; v20.2.4 `:4450-4460`), and
    CompleteMultipartUpload stores the object under that policy.
- **Impact:** a bucket owner's BlockPublicAcls does not stop a writer from
  making new objects public, through grant headers on PutObject or any ACL
  on CopyObject and CreateMultipartUpload; S3 documents the setting as
  refusing PUT Object calls whose ACL is public. The object is then public
  in fact where IgnorePublicAcls is off: a read adds the AllUsers grant's
  permissions unless the bucket ignores public ACLs (`rgw_acl.cc:180-186`
  at both tags; `rgw_common.cc:1572-1575`, v20.2.4 `:1626-1629`), so an
  anonymous GET succeeds. The writer is one the owner allows to write but
  not to publish: another account, a less privileged user or a tenant.
- **Releases:** checked at v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it: PutObject, CopyObject,
  CreateMultipartUpload and UploadPart refuse a public policy under the
  block, whatever headers set it (`internal/op/putobject.go`,
  `internal/op/copyobject.go`, `internal/op/initmultipart.go`,
  `internal/op/uploadpart.go`; `docs/exclusions.md`, "A block of public
  ACLs refuses a public ACL on PutObject, CopyObject, CreateMultipartUpload
  and UploadPart").
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-07; awaiting response. rgw-bug-reproduction's prior-art search
  found no report or fix. The nearest work covers the canned ACLs alone:
  [#49135](https://tracker.ceph.com/issues/49135) and its fix,
  [ceph/ceph#64290](https://github.com/ceph/ceph/pull/64290), "rgw/s3: fix
  PutObject's canned_acl comparisons for BlockPublicAcls".
- **Found:** phase 1 unit W, Task 10, 2026-10-05, auditing the write ops'
  public-access checks; derived from the source, not reproduced.

## RESTArgs::get_string url-decodes an admin argument a second time

- **Kind:** defect, low; unfixed at v19.2.6 and v20.2.4; main not checked.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`.
  - `RGWHTTPArgs::parse` url-decodes each query pair as a query, so `%2B`
    is already '+' and `%25` already '%' in the value it stores
    (`rgw_common.cc:848-898` at v19.2.6).
  - `RESTArgs::get_string`, which the admin ops read their string
    arguments through, decodes that value again with `in_query` set
    (`rgw_rest.cc:854-871` at v19.2.6, `:859-876` at v20.2.4).
  - A value the client encoded once is decoded twice: `uid=a%2Bb` reaches
    the op as `a b`, and `uid=a%2525b` as `a%b`.
- **Impact:** an admin client that encodes its arguments once, as go-ceph's
  rgw/admin package does through `url.Values`, cannot name a user, bucket
  or key whose name holds '+' or '%': the op receives another name, and a
  '%' followed by text that is not hex empties the value.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** mirrors it (`Args.String`, `internal/admin/request.go`), so
  an admin request means the same with either gateway; `Args.Get` keeps
  the single decoding the handlers' own `args.get` calls see.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit N, Task 3, 2026-10-05, transcribing `RESTArgs`;
  derived from the source, not reproduced.

## radosgw's integer argument readers refuse their type's maximum and truncate to 32 bits

- **Kind:** defect, low; unfixed at v19.2.6 and v20.2.4; main not checked.
  Unreproduced: derived from the source.
- **Evidence:** `stringtoll`, `stringtoull`, `stringtol` and `stringtoul`
  (`src/rgw/rgw_string.h:38-100` at v19.2.6 and v20.2.4, unchanged), which
  `RESTArgs::get_int64`, `get_uint64`, `get_int32` and `get_uint32` call
  (`src/rgw/rgw_rest.cc:873-955` at v19.2.6):
  - each takes its C conversion's saturated overflow value as the error
    marker, so the type's own maximum, written out, is refused too:
    `18446744073709551615` for a u64, `9223372036854775807` for an i64;
  - `strtoull` negates a negative text, so `-2` reads as 2^64-2 and only
    `-1` is refused;
  - `stringtol` and `stringtoul` convert with the 64-bit `long` and
    `unsigned long` and cast the result to 32 bits unchecked, so
    `4294967296` reads as 0 and `4294967297` as 1;
  - an empty value reads as 0, since `strtoll` leaves nothing behind it.
- **Impact:** an admin argument out of its type's range is taken as
  another value rather than refused, such as a 32-bit count of 2^32 read
  as 0.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** mirrors it (`Args.Int64`, `Int32`, `Uint64` and `Uint32`,
  `internal/admin/request.go`).
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit N, Task 3, 2026-10-05, transcribing `RESTArgs`;
  derived from the source, not reproduced.

## HTMLFormatter formats a long value from an ended va_list

- **Kind:** defect, low; unfixed at v19.2.6 and v20.2.4; main not checked.
  Unreproduced: derived from the source.
- **Evidence:** `HTMLFormatter::dump_format_va` copies `ap` to `ap_copy`,
  formats from `ap`, ends `ap_copy`, and, when the text did not fit its
  1024-byte buffer, formats again from the ended `ap_copy`
  (`src/common/HTMLFormatter.cc:141-168` at v19.2.6 and v20.2.4, unchanged).
  JSONFormatter and XMLFormatter format from the copy first and keep `ap`
  for the retry (`src/common/Formatter.cc:348-365` and `:544-571` at
  v19.2.6). The C standard leaves a va_list used after `va_end` undefined.
- **Impact:** a boolean or unquoted value of 1024 bytes or more in an HTML
  response, which radosgw's admin API can write for a zone's tier
  configuration, is formatted through undefined behaviour. With GCC on
  x86-64 and AArch64, where `va_end` does nothing, the retry reads the
  intact copy and the output is right.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** unaffected; `NewHTML` (`internal/formatter/html.go`) writes
  the value whole.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit N, Task 3, 2026-10-05, transcribing
  HTMLFormatter; derived from the source, not reproduced.

## radosgw drops a role or session policy that does not parse, and evaluates the request without it

- **Kind:** defect, security-relevant: a privilege escalation under version
  skew, where a role session's policy is dropped and its scope-down with
  it; unfixed through main. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RoleApplier::modify_request_state` parses the role's inline policies,
    its managed policies and the session policy its token carries, each in
    a `try` whose `catch (PolicyParseException&)` logs at level 20 and goes
    on, under the comment "Control shouldn't reach here as the policy has
    already been verified earlier" (`rgw_auth.cc:1203-1240`, `:1248-1285`;
    the catches at `:1212`, `:1223` and `:1235`, `:1257`, `:1268` and
    `:1280`; main `:1324`, `:1335` and `:1347` at 7ed73efc1be). A policy
    that does not parse is dropped, and the request is evaluated without
    it.
  - Without a session policy, `evaluate_iam_policies` skips the
    session-policy intersection altogether
    (`if (!session_policies.empty())`, `rgw_common.cc:1194`, `:1207`; main
    `:1214`), so the role's own policies grant what they grant, with no
    scope-down.
  - A policy that parsed where it was written fails where it is read when
    the reader's parser is older: v19.2.6 knows 150 policy actions, v20.2.4
    160 and main 179 (`actpairs`, `rgw_iam_policy.cc:64` at each), and an
    action a gateway does not know fails the parse ("radosgw terminates on
    a copy source or system request whose bucket policy does not parse"). So a
    session policy that a Tentacle gateway's AssumeRole accepted, naming a
    Tentacle-only action, is dropped by a Squid gateway serving the
    token's requests in the same zone, as during a rolling upgrade.
  - A bucket policy that does not parse is refused instead: the request
    answers 403 (`rgw_op.cc:598-615`, `:628-645`).
- **Impact:** a role session runs with the role's full permissions instead
  of the intersection its session policy asked for, wherever its session
  policy does not parse; a role whose inline or managed policy does not
  parse loses that policy's Deny statements along with its grants. Whether
  any path stores policy text without parsing it first (multisite metadata
  sync, a raw `radosgw-admin` put, a restore), which would reach the same
  drop without version skew, is open and needs a cluster.
- **Releases:** v19.2.6, v20.2.4 and main.
- **rgw-go:** not reached yet: phase 1 has no STS and applies no role or
  session policy. The task that adds them must not reproduce the silent
  drop without an owner ruling, and records its choice in
  `docs/exclusions.md`.
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-05; awaiting response. rgw-bug-reproduction's prior-art search
  found no report or fix; [#78697](https://tracker.ceph.com/issues/78697),
  [#73659](https://tracker.ceph.com/issues/73659) and
  [#74392](https://tracker.ceph.com/issues/74392) are distinct, and the
  sibling [#81253](https://tracker.ceph.com/issues/81253), whose fix
  [ceph/ceph#72271](https://github.com/ceph/ceph/pull/72271) touches only
  `rgw_op.cc`, leaves these handlers as they are. It is unfiled while filing
  is paused.
- **Found:** rgw-bug-reproduction, 2026-10-04, while triaging "Tentacle's
  radosgw aborts on a policy whose Statement is a string"; derived from
  the source, not reproduced.

## A refused CompleteMultipartUpload drops its parts' index entries

- **Kind:** defect, unfixed at v19.2.6 and v20.2.4. Not a security issue.
  Reproduced upstream (tracker #80907); derived from the source here.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RadosMultipartUpload::complete` collects every part's index entry, and
    those of each part's earlier uploads, in `remove_objs` and hands them to
    the head's `write_meta` (`rgw/driver/rados/rgw_sal_rados.cc:3568-3573`
    and `:3624`; `:4416-4421` and `:4472`).
  - When the head write fails, `done_cancel` cancels the index change with
    those `remove_objs` (`rgw/driver/rados/rgw_rados.cc:3369-3380`;
    `:3522-3533`), and cls_rgw's `bucket_complete_op` removes the entries
    `remove_objs` names on every completion, a cancel included
    (`cls/rgw/cls_rgw.cc:1095-1126` and `:1213-1227`; `:1230-1260` and
    `:1344-1358`).
  - A completion refused at the head write therefore keeps its upload, which
    can be listed, completed again or aborted, but its parts have no index
    entries: If-None-Match: * losing the race to create the key, or If-Match:
    * finding it removed, on Tentacle, which honours the conditions
    (`rgw/driver/rados/rgw_sal_rados.cc:4481-4482` at v20.2.4), and any
    write the OSD refuses on both releases.
- **Impact:** the bucket's stats undercount the parts' objects and bytes
  until the upload is completed or aborted. No data is lost.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. It sends the parts' entries with the
  meta object's removal, after the head is written, not with the head's
  index change (`Store.Complete`, `internal/driver/mp_complete.go`;
  `docs/exclusions.md`, "CompleteMultipartUpload checks the part heads and
  keeps the parts until its head is written").
- **Upstream:** [#80907](https://tracker.ceph.com/issues/80907) (Fix Under
  Review); the tracker names
  [ceph/ceph#72109](https://github.com/ceph/ceph/pull/72109) as its fix.
- **Found:** phase 1 unit P, Task 6, 2026-10-05, ordering the completion's
  head write and part retirement; upstream reported it first.

## A multipart completion whose meta object outlives its head write lets a retry or an abort delete the object's data

- **Kind:** defect, unfixed at v19.2.6 and v20.2.4: data loss. Reproduced
  upstream (tracker #80896); derived from the source here.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWCompleteMultipart::execute` answers success once the head is
    written, and removes the meta object afterwards; a removal that keeps
    failing is only logged (`rgw_op.cc:6482-6530`; `:7366-7420`). A
    gateway that stops between the two leaves the meta object as well.
  - A retry then takes the lock, which the meta object still carries;
    `check_previously_completed` runs only when the lock meets no meta
    object (`rgw_op.cc:6435-6446`; `:7249-7260`). `complete` lists the same
    parts and writes the head again, and `write_meta`'s
    `complete_atomic_modification` queues the replaced head's tails, which
    are those parts, for the GC (`driver/rados/rgw_rados.cc:3314-3317`;
    `:3467-3470`).
  - An abort, or lifecycle's, queues every part the meta object lists for
    the GC (`driver/rados/rgw_sal_rados.cc:3151-3263`; `:3995-4107`).
  - Before the head write, each part's earlier uploads are queued for the
    GC (`cleanup_part_history`, `driver/rados/rgw_sal_rados.cc:3573`;
    `:4421`), so a retry that its conditions then refuse still removes
    parts the object holds when a part was uploaded again since.
- **Impact:** once the GC runs, the completed object's head and manifest
  stand but its data is gone, and a GET answers 404 NoSuchKey. A principal
  allowed s3:AbortMultipartUpload on the bucket but not s3:DeleteObject
  reaches the same through an abort, given such a leftover upload.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it for an upload rgw-go completes, and
  reproduces it in two mixed-fleet cases: a retry, by rgw-go, of an upload
  whose meta object a floor radosgw left behind and whose object was then
  overwritten or deleted, since that meta object carries no record and the
  head no longer names the upload; and a floor radosgw's retry or abort of
  an upload rgw-go recorded, since a floor radosgw ignores the record. It
  writes the completion record of
  ceph/ceph#72103, as that PR stands at its second commit (2026-09-28), on
  the meta object before the head write: `user.rgw.mp_completion_tag`, the
  head's idtag with its NUL, and `user.rgw.mp_completion_instance`,
  `null`, in the op that renews the lock, and removes them after a head
  write that certainly failed. A completion that finds a record finishes
  the earlier one, writing no head, only when the key's head carries the
  recorded tag and the ETag the request's parts make, and answers 404
  NoSuchUpload otherwise, so a retry after the object was overwritten or
  deleted writes nothing over the parts queued for the GC (`replayRecorded`
  in `internal/driver/mp_complete.go`; `docs/exclusions.md`, "A
  CompleteMultipartUpload records itself on the meta object before its head
  write"). As in the PR, a retry after a crash between the record and the
  head write, or after a head write that timed out and never landed, is
  refused, and the upload has to be aborted. A meta object a floor radosgw
  left carries no record: rgw-go then reads the key's head and finishes a
  head that names the upload's objects only when it is the object the
  completion would write, refusing any other with 404 NoSuchUpload
  ("CompleteMultipartUpload finishes an earlier completion of its upload
  instead of writing it again"). The part history is queued only after the
  head write. rgw-go's AbortMultipartUpload honors the record as the PR's
  abort does: when the key's head carries the recorded tag it removes the
  meta object alone and queues no part. Without a record it does not
  reproduce the abort half either, where v19.2.6 and v20.2.4 queue the
  object's parts: when the key's head names an object of the upload, it
  answers 404 NoSuchUpload and changes nothing (`Store.Abort`,
  `internal/driver/mp_abort.go`; `docs/exclusions.md`,
  "AbortMultipartUpload leaves a completed upload's parts alone"). The
  record's format is the PR's at its head; it is revisited when the PR
  merges.
- **Upstream:** [#80896](https://tracker.ceph.com/issues/80896) (Fix Under
  Review); the tracker names
  [ceph/ceph#72103](https://github.com/ceph/ceph/pull/72103) as its fix.
  Related: [#75375](https://tracker.ceph.com/issues/75375). A review
  comment on the PR,
  [ceph/ceph#72103 (comment)](https://github.com/ceph/ceph/pull/72103#issuecomment-6008906564),
  reports that its replay of a recorded completion sends the completed
  object's own parts to the GC when an UploadPart races the replay: the
  replay returns before it fills the set of parts to keep, and the
  orphan cleanup that follows a lost meta-object removal then queues
  every part. rgw-go's replay builds that set from the head's manifest
  (`replayRecorded` in `internal/driver/mp_complete.go`).
- **Found:** phase 1 unit P, Task 6, 2026-10-05, reading the tracker as the
  task's notes asked; upstream reported it first.

## radosgw's CompleteMultipartUpload does not keep its lock past rgw_mp_lock_max_time

- **Kind:** defect, fixed on main after v19.2.6 and v20.2.4, its backports
  pending. Unreproduced here: derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `MPRadosSerializer::try_lock` takes the `RGWCompleteMultipart` lock for
    `rgw_mp_lock_max_time` (`driver/rados/rgw_sal_rados.cc:3750-3760`;
    `:4613-4624`), and nothing renews or checks it before the head write
    (`rgw_op.cc:6428-6489`; `:7242-7373`).
  - A completion that runs longer than the lock, ten minutes by default,
    can meet a second completion of the same upload that took the lapsed
    lock; the later head write replaces the earlier one and queues its
    tails, the same parts, for the GC.
  - Main renews the lock every half duration and refuses the head write with
    500 InternalError, "This multipart completion is already in progress",
    when a renewal failed (merge c1f7de9e4e6, not an ancestor of v20.2.4).
- **Impact:** a completed object whose data the GC removes; reached only by
  a completion slower than `rgw_mp_lock_max_time`.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** renews the lock with `LOCK_FLAG_MUST_RENEW` just before the
  head write and answers the same 500 when it no longer holds it
  (`Store.renewMeta`, `internal/driver/mp_cleanup.go`; `docs/exclusions.md`,
  "CompleteMultipartUpload renews its lock before the head write").
- **Upstream:** [#75375](https://tracker.ceph.com/issues/75375)
  (Backporting); fixed by
  [ceph/ceph#67696](https://github.com/ceph/ceph/pull/67696).
- **Found:** phase 1 unit P, Task 6, 2026-10-05, through tracker #80896,
  which names it; upstream reported it first.

## radosgw sends several GC chains under one tag, and an omap-era gc shard keeps only the last

- **Kind:** defect, unfixed at v19.2.6 and v20.2.4: a leak, on the omap
  log's backend only, not security-relevant. Unreproduced: derived from
  the source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWGC::send_chain` falls back to `cls_rgw_gc_set_entry` on a gc shard
    still on the omap log (`rgw/driver/rados/rgw_gc.cc:120-139` at both
    tags), and the class keys an omap entry by its tag: `gc_update_entry`
    replaces the entry already under the tag, chain included
    (`cls/rgw/cls_rgw.cc:3896-3941`; `:4298-4343`).
  - radosgw sends several chains under one tag: `send_split_chain`'s batches
    of a large chain (`rgw/driver/rados/rgw_gc.cc:68-118` at both tags), and
    for a multipart upload, the upload id for each part's earlier uploads in
    `cleanup_part_history`, for the orphans of each retry in
    `cleanup_orphaned_parts`, and for the parts in `abort`
    (`rgw/driver/rados/rgw_sal_rados.cc:3132-3146`, `:3085-3099` and
    `:3222-3236`; `:3976-3990`, `:3929-3943` and `:4066-4079`).
- **Impact:** on such a shard, every chain but the last sent under a tag is
  never collected, and its objects leak. Only shards still on the omap log
  are affected ("cls_rgw's omap gc log loses an entry due in the same
  nanosecond as another" says when that is). The queue, the default
  backend, keeps every chain, since it enqueues by position, and
  `RGWGC::initialize` moves every shard to it at startup
  (`rgw/driver/rados/rgw_gc.cc:39-55`, `:53` at v20.2.4 for
  `gc_log_init2`), so the leak lives only on a shard not yet moved.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** reproduces it on a gc shard still on the omap log, for a
  split chain and for the GC entries of one upload's completion and its
  retries, which it sends with the same class calls and the same fallback
  (`enqueueGC` and `sendGCChain`, `internal/driver/gc_enqueue.go`). It is
  immune on a queue shard, and moves every shard to the queue at startup
  as radosgw does (`gcInitialize`, `internal/driver/gc.go`). A
  completion's part history goes in one entry, where radosgw sends one per
  part uploaded again. An abort sends its parts in one chain, once the meta
  object is removed, where radosgw sends one per round of its retry after a
  racing part (`Store.Abort`, `internal/driver/mp_abort.go`); on an omap-log
  shard it meets the defect only for a chain long enough to split, or for
  an upload whose earlier completion queued part history under the same
  upload id before its object was overwritten.
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix; it places the entry in the lineage of the gc work in
  [ceph/ceph#28421](https://github.com/ceph/ceph/pull/28421),
  [ceph/ceph#46020](https://github.com/ceph/ceph/pull/46020) and
  [ceph/ceph#50206](https://github.com/ceph/ceph/pull/50206), and apart from
  [#68169](https://tracker.ceph.com/issues/68169) and
  [#77098](https://tracker.ceph.com/issues/77098). The fix would merge the
  prior chain into the entry on a same-tag update of the omap log.
- **Found:** phase 1 unit P, Task 6, 2026-10-05, sending a completion's part
  history to the GC; derived from the source, not reproduced.

## A CompleteMultipartUpload that loses its head write's race leaks its parts

- **Kind:** defect, unfixed at v19.2.6 and v20.2.4: a leak. Unreproduced:
  derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `done_cancel` answers a head write that met a head another writer
    replaced, removed or created as success when the write carries no
    conditions, and does the same for If-Match: * over a replaced head and
    If-None-Match: * over a removed one (`driver/rados/rgw_rados.cc:3382-3414`;
    `:3534-3567`). Its cancel removes the parts' index entries with the
    completion's `remove_objs` (`:3374`; `:3527`).
  - `RadosMultipartUpload::complete` returns that 0
    (`driver/rados/rgw_sal_rados.cc:3635-3639`; `:4485-4489`), and
    `RGWCompleteMultipart::execute` removes the meta object and answers 200
    (`rgw_op.cc:6491-6530`; `:7381-7420`).
  - The parts' current head and stripe objects are then named by no head,
    no meta object, no index entry and no GC entry. Only each part's
    earlier uploads, those of a part uploaded again, are queued for the GC
    under the upload id, which `cleanup_part_history` does before the head
    write (`driver/rados/rgw_sal_rados.cc:3573`; `:4421`).
- **Impact:** the parts' bytes stay in the data pool for good, outside every
  listing and every quota count, and no tool radosgw ships finds them short
  of an orphan scan of the pool. Nothing is lost: the client's object is
  the racing writer's, as for any write that loses a race.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** reproduces it, answering 200 and removing the meta object
  with the parts' entries, and queues only the parts' earlier uploads for
  the GC, after the head write rather than before it: the writer that won
  the race may be another completion of the same upload, whose head names
  the parts' current objects (`Store.Complete`,
  `internal/driver/mp_complete.go`).
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit P, Task 6 review, 2026-10-05, reading
  `done_cancel` for the completion's lost race; derived from the source, not
  reproduced.

## radosgw's PutBucketAcl takes any canned ACL whose name holds "bucket" for private

- **Kind:** defect, low; unfixed at v19.2.6 and v20.2.4; main not checked.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`.
  - `RGWPutACLs_ObjStore_S3::get_policy_from_state` clears a bucket
    request's canned ACL when `s->canned_acl.find("bucket")` finds the text
    anywhere in it (`rgw_rest_s3.cc:3640-3644` at v19.2.6, `:3923-3927` at
    v20.2.4), meaning to ignore `bucket-owner-read` and
    `bucket-owner-full-control`, which name the bucket's owner and do not
    apply to a bucket.
  - The cleared name reaches `create_canned` as empty, which builds the
    private ACL (`rgw_acl_s3.cc:420`), so a name radosgw does not know but
    which holds "bucket", such as `x-amz-acl: nobucketsuch`, is never seen
    by `create_canned`'s refusal of an unknown name (`rgw_acl_s3.cc:450-452`
    at v19.2.6 and v20.2.4). Nothing checks the header's value earlier: it
    is read as is (`rgw_rest_s3.cc:5020-5022` at v19.2.6, `:5580-5582` at
    v20.2.4).
  - An object's PutObjectAcl keeps the name, so the same header there
    answers 400 InvalidArgument.
- **Impact:** a PutBucketAcl whose canned ACL is misspelled but holds
  "bucket" answers 200 and replaces the bucket's ACL with the private one,
  where any other unknown name answers 400 InvalidArgument and leaves the
  ACL as it was. The result is no more open than before: the private ACL
  grants only the owner, and its FULL_CONTROL gives the owner nothing it
  lacked, since a bucket's owner can always rewrite the ACL through the
  WRITE_ACP an owner holds implicitly (`rgw_acl.cc:172` at v19.2.6 and
  v20.2.4).
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** reproduces it (`(*authz.Evaluator).BuildACL`,
  `internal/authz/write.go`), so a bucket ACL request means the same with
  either gateway.
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit Z, Task 11, 2026-10-05, transcribing
  `RGWPutACLs::execute` for `BuildACL`; derived from the source, not
  reproduced.

## Tentacle's radosgw terminates on a GET or HEAD of an object whose restore attr does not decode

- **Kind:** defect, unfixed through main: robustness, not
  security-relevant, since no client request writes these attrs.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/`, at v20.2.4; v19.2.6 has no restore
  attrs and no such code.
  - `RGWGetObj_ObjStore_S3::send_response_data` reads an object's restore
    state for the `x-amz-restore` header with three bare `decode` calls
    (`rgw/rgw_rest_s3.cc:588-614`): the `user.rgw.restore-status` attr
    (`:593`), then `user.rgw.restore-type` (`:604`), then the
    `user.rgw.restore-expiry-date` attr (`:613`), not `restored-at`
    (`RGW_ATTR_RESTORE_STATUS`, `RGW_ATTR_RESTORE_TYPE` and
    `RGW_ATTR_RESTORE_EXPIRY_DATE`, `rgw/rgw_common.h:129-132`). No `try`
    surrounds them, unlike the replication and checksum decodes before
    them (`:512-519`, `:523-527`, `:553-584`).
  - The status and type are one-byte enums, which `denc` decodes with
    `p.copy(sizeof(T), ...)` (`include/denc.h:305-330`), and the expiry date
    is a `real_time` of eight bytes. An empty status or type attr, or an
    expiry date shorter than eight bytes, throws `buffer::end_of_buffer`.
  - `send_response_data` runs on every successful GET and HEAD: for a HEAD
    and for a GET with nothing to read from `RGWGetObj::execute`
    (`rgw/rgw_op.cc:2668-2669`), and for any other GET from `get_data_cb`
    (`:2390-2394`) inside `read_op->iterate` (`:2675`) or at the end
    (`:2686`).
  - Nothing above catches it. `rgw_process_authenticated` calls
    `op->execute(y)` bare (`rgw/rgw_process.cc:258`), and `process_request`
    catches only `ceph::crypto::DigestException` around the op (`:345` to
    `:417`) and `rgw::io::Exception` around the request's completion
    (`:460-467`). The beast
    frontend's connection coroutine rethrows whatever escapes it
    (`rgw/rgw_asio_frontend.cc:1117` and `:1134`), on an `io_context_pool`
    thread that catches nothing (`common/async/context_pool.h:68` and
    `:83`), so `std::terminate` ends the process, as in "radosgw terminates
    on a stored lz4 block too short for its pair table", above.
- **Impact:** a GET or HEAD of such an object, by anyone allowed to read it,
  anonymous readers of a public object included, ends the radosgw process
  and every request it is serving, and does so again on each retry.
- **Reachability:** radosgw writes the three attrs whole when a restore
  sets them (`rgw/driver/rados/rgw_rados.cc:5635`, `:5656`, `:5664`,
  `:5692`), and an S3 client's metadata lands under `user.rgw.x-amz-meta-`
  (`RGW_ATTR_META_PREFIX`, `rgw/rgw_common.h:93`); RestoreObject only
  starts the restore. So it takes a writer with access to the data pool, a
  corrupted attr, or a bug in the restore machinery that leaves an attr
  short.
- **Releases:** v20.2.4 and main e6dd4bc2a2c, whose three decodes are as
  bare (`rgw/rgw_rest_s3.cc:622`, `:633` and `:642`). v19.2.6 is not
  affected.
- **rgw-go:** not affected. Its GET and HEAD handler decodes each attr
  before it writes the header and omits `x-amz-restore` for one that does
  not decode (`internal/s3/getobject.go`; `docs/exclusions.md`, "A restore
  attr that does not decode").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix; only the PRs that built the restore feature touch this
  code ([ceph/ceph#59311](https://github.com/ceph/ceph/pull/59311),
  [ceph/ceph#62713](https://github.com/ceph/ceph/pull/62713),
  [ceph/ceph#67547](https://github.com/ceph/ceph/pull/67547)). The
  hardening would wrap the block in a `try` that catches `buffer::error`,
  as the neighbouring decodes do.
- **Found:** phase 1 unit R, Task 7, 2026-10-05, porting
  `send_response_data`'s headers; derived from the source, not reproduced.

## radosgw sends the ACL document as a body after the headers of a HEAD ?acl

- **Kind:** defect, protocol conformance, low: informational, not a
  request-smuggling vector. Confirmed on the wire by rgw-bug-reproduction;
  not reproduced here.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - A HEAD with `?acl` runs `RGWGetACLs_ObjStore_S3`, at object scope
    (`RGWHandler_REST_Obj_S3::op_head`, `rgw_rest_s3.cc:4823-4831`,
    `:5380-5388`) and at bucket scope alike.
  - Its `send_response` ends the header with no length, flushes the
    formatter through `rgw_flush_formatter`, which skips a HEAD, and then
    writes the policy with `dump_body(s, acls)`, which does not
    (`rgw_rest_s3.cc:3605-3614`, `:3888-3897`; `rgw_flush_formatter`,
    `rgw_rest.cc:316-324` at both; `dump_body`, `rgw_rest.cc:781-800`,
    `:786-805`). `acls` holds the policy XML `RGWGetACLs::execute` wrote
    (`rgw_op.cc:5725-5734`, `:6305-6314`).
  - No layer below drops it. The beast frontend's filters buffer a body
    sent without a length (`BufferingFilter::complete_header`,
    `rgw_client_io_filters.h:208-219` at both) and send it with a
    `Content-Length` of its size when the request completes
    (`BufferingFilter::complete_request`, `:221-253` at both), and neither
    those filters nor the beast client (`rgw_asio_client.cc`) looks at the
    method.
- **Impact:** the HEAD response carries the ACL document, without its XML
  declaration, as a body of exactly the length its `Content-Length` names,
  so the framing stays consistent. A peer that reads the body by that
  length stays in step. One that reads no body after a HEAD, as RFC 9110,
  section 9.3.2, and RFC 9112, section 6.3, require, finds the bytes left
  on the connection; mature clients and proxies close such a connection.
  Only an intermediary that both reads no body and pools the connection
  without checking for leftover bytes would hand them to another request,
  which rgw-bug-reproduction's triage judged theoretical and unproven. Any
  reader of the ACL can send the HEAD, at object and bucket scope.
- **Releases:** checked at v19.2.6 and v20.2.4.
- **rgw-go:** not affected: it writes no body for a HEAD
  (`writeDocument`, `internal/s3/objectdocs.go`). rgw-go answers HEAD ?acl
  with the GET's headers, whose `Content-Length` counts the XML
  declaration as well (`docs/exclusions.md`, "HEAD ?acl").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix. The fix would guard `dump_body` on a HEAD, as
  `rgw_flush_formatter` already is.
- **Found:** phase 1 unit R, Task 7, 2026-10-05, porting
  `RGWGetACLs_ObjStore_S3::send_response`; derived from the source, not
  reproduced.

## radosgw stores a POST upload's x-amz-meta fields with their CR and LF and sends them raw on GET

- **Kind:** defect, security-relevant: response-header injection and
  response splitting (CWE-113), triage estimate CVSS about 6.1. Unfixed
  at v19.2.6 and v20.2.4. Unreproduced: derived from the source and a
  model of the code; no cluster.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWPostObj_ObjStore_S3::get_params` reads each form field's body with
    `read_data` (`rgw_rest_s3.cc:2955`, `:3089`), which looks for the
    boundary and for no line end: it calls `read_with_boundary` with
    `check_crlf` false (`rgw_rest.cc:1297-1303` and `:1217-1287`,
    `:1302-1308` and `:1222-1292`). A field's value keeps every CR and LF
    inside it.
  - Each `x-amz-meta-*` field's bytes become the object's attr of that
    name, with a NUL appended and nothing checked
    (`rgw_rest_s3.cc:3019-3038`, `:3166-3185`).
  - A GET or HEAD of the object writes each such attr as a header
    (`send_response_data`, `rgw_rest_s3.cc:581-585`, `:698-702`) through
    `dump_header`, whose `rgw_sanitized_hdrval` drops only a trailing NUL
    (`rgw_rest.cc:359-363` and `rgw_rest.h:29-47` at both tags). Then
    `ClientIO::send_header` writes the name, `: `, the value and a CRLF as
    they are (`rgw_asio_client.cc:167-181` at both tags).
  - The fix for CVE-2020-1760 refuses a control character only in a GET's
    response-* query parameters (`str_has_cntrl`, `rgw_rest_s3.cc:529`,
    `:646`). Nothing checks a stored value.
- **Trigger:** a POST upload whose `x-amz-meta-*` field holds a CR LF and a
  header line, or a CR LF CR LF and text. The uploader needs only the right
  to POST to the bucket. A POST policy must name every field in a
  condition (`match_policy_vars`, `rgw_policy_s3.cc:105-121`, `:108-130`),
  but a `starts-with` condition passes any value that begins with its
  prefix, an empty one included (`:64-73`, `:68-77`). A form with no policy
  runs as the anonymous user (`get_policy`, `rgw_rest_s3.cc:3230-3232`,
  `:3374-3376`), which a bucket that grants public writes admits.
- **Impact:** every reader of the object, by GET or HEAD, receives the
  headers the uploader wrote, such as a `Set-Cookie` or a second
  `Content-Type`. An empty line ends the header section early, and the
  uploader's text becomes the start of the body (response splitting). A
  site that hands its users POST policies so that browsers upload straight
  to radosgw lets any of those users plant such a response for the others.
- **Releases:** v19.2.6 and v20.2.4. Not checked on main.
- **rgw-go:** not affected. It serves no POST upload yet, and its GET and
  HEAD write a stored value through net/http, which rewrites each CR and LF
  in a header value to a space and trims the value's ends
  (`net/http/header.go:139` and `:206-207` at Go 1.27.1). So a value that
  radosgw stored this way reaches the client as one header
  (`docs/exclusions.md`, "A stored header value's CR and LF go out as
  spaces").
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-07; awaiting response. rgw-bug-reproduction's prior-art search
  found no report or fix. CVE-2020-1760 is the response-* parameter
  variant, and its fix does not reach a stored value. The fix would refuse,
  or strip, control characters in a POST upload's metadata fields.
- **Found:** phase 1 unit R, Task 7 review, 2026-10-05, tracing a POST
  upload's metadata to the GET that sends it; confirmed by
  rgw-bug-reproduction's triage against a model of the code. Not
  reproduced on a running system.

## radosgw's AbortMultipartUpload queues the parts for the GC before it removes the upload

- **Kind:** defect, robustness (triage estimate CVSS about 2.2), found
  upstream first as a facet of tracker #80896. Unreproduced: derived from
  the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RadosMultipartUpload::abort` sends every part's objects to the GC
    under the upload id (`driver/rados/rgw_sal_rados.cc:3222-3236`;
    `:4066-4080`), and only then removes the meta object, with the parts'
    index entries, behind the version it read (`:3238-3250`; `:4082-4094`).
  - A completion cannot run beside the abort: both take the exclusive
    `RGWCompleteMultipart` lock (`rgw_op.cc:6434-6435` and `:6640-6641`;
    `:7248-7249` and `:7564-7565`), which `MPRadosSerializer::try_lock`
    takes with assert_exists and lock_exclusive
    (`driver/rados/rgw_sal_rados.cc:3750-3760`; `:4613-4624`). The defect
    is in the windows where the parts are queued and the upload still
    stands once the abort is over:
    - a removal that fails other than with ECANCELED ends the abort with
      its error (`:3251-3256`; `:4095-4100`), and so does the fifteenth
      ECANCELED from parts that keep racing it, which falls out of the loop
      (`:3257-3262`; `:4101-4106`); the removal's cancel also drops the
      parts' index entries (`driver/rados/rgw_rados.cc:5969`; `:6723`);
    - a gateway that stops between the GC send and the removal;
    - an abort that runs longer than `rgw_mp_lock_max_time`: neither it nor
      `RGWAbortMultipart::execute` renews or checks the lock
      (`rgw_op.cc:6637-6649`; `:7561-7573`), so a completion can take the
      lapsed lock and write its head over the parts the abort queues.
  - In the first two windows the meta object and the part infos remain, so
    ListParts lists the parts and a later CompleteMultipartUpload, which
    checks only the part infos (`driver/rados/rgw_sal_rados.cc:3433-3640`;
    `:4279-4490`), writes a head over the queued objects.
- **Impact:** once `rgw_gc_obj_min_wait`, two hours by default, has passed,
  the GC removes the parts, and the completed object's head stands over
  nothing: a GET answers 404 NoSuchKey. Reaching it takes a failed removal,
  fifteen racing part uploads, a gateway that stops mid-abort, or an abort
  slower than the lock, followed by a completion of the same upload.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce the first two windows. It removes the meta
  object first and queues the parts once the meta object is gone, and a
  failed removal keeps the parts' index entries. It closes the third with
  a lock cookie of each abort's own, which the OSD checks: the abort takes
  the lock under it, and the removal asserts that hold in its own op, so
  the OSD refuses the removal, however late it runs, once the abort's hold
  lapsed, even when a completion of the same gateway, the same entity
  under cookie "", took the lock since (`cls/lock/cls_lock.cc:174-178`,
  `:201-212` and `:507-519` at both tags); the abort then answers 503 and
  queues nothing (`Store.Abort`, `internal/driver/mp_abort.go`;
  `docs/exclusions.md`, "AbortMultipartUpload removes the upload before it
  frees the parts").
- **Upstream:** [#80896](https://tracker.ceph.com/issues/80896) (Fix
  Under Review) covers it as one facet. Its fix,
  [ceph/ceph#72103](https://github.com/ceph/ceph/pull/72103), does not: at
  2c1db8239cc its abort still sends the chain to the GC before `delete_obj`
  (`driver/rados/rgw_sal_rados.cc:4311-4348`), so the fix PR for this facet
  is none.
- **Found:** phase 1 unit P, Task 7, 2026-10-05, ordering the abort's GC
  enqueue and meta object removal; upstream reported it first, as part of
  #80896; derived from the source, not reproduced.

## radosgw's bucket delete aborts each page of multipart uploads again on every later page

- **Kind:** defect, unfixed at v19.2.6 and v20.2.4: wasted work and a
  wrong count. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/driver/rados/`; each pair of lines
  is v19.2.6's, then v20.2.4's.
  - `RadosBucket::abort_multiparts` declares its `uploads` vector once,
    before the loop that lists a page of 1000 meta objects at a time
    (`rgw_sal_rados.cc:963` and `:970-1010`; `:983` and `:990-1030`).
  - The marker advances: `marker = params.marker.name` moves each listing
    past the last page (`:953`; `:973`). Only the accumulator is stale:
    `list_multiparts` appends each page's uploads to that vector and never
    clears it (`:945-946`; `:965-966`), so the loop over `uploads` on page k
    aborts the uploads of pages 1 to k-1 again (`:985-1003`; `:1005-1023`).
  - Each repeated abort reads the removed meta object's attrs, gets ENOENT
    and answers `-ERR_NO_SUCH_UPLOAD` (`RadosMultipartUpload::abort`,
    `:3167-3172`; `:4011-4016`), which the loop logs, skips and counts in
    `num_deleted` (`:990-1002`; `:1010-1022`); the WARNING after each page
    logs that running count (`:1004-1008`; `:1024-1028`).
- **Impact:** deleting a bucket whose uploads fill k pages makes
  500·k(k-1) extra meta-object reads, about N²/2000 for N uploads, and the
  last logged count of aborted uploads is too high by as many: 1003
  uploads report 1000 after the first page and 2003 after the second.
  Nothing is lost: an upload aborted again is already gone. No upload id
  repeats, so a repeated abort cannot meet a new upload.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. Its bucket delete aborts each listed
  upload once and counts it once (`abortMultiparts`,
  `internal/driver/mp_abort.go`; `docs/exclusions.md`, "A bucket delete
  aborts its multipart uploads under their completion lock").
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** phase 1 unit P, Task 7, 2026-10-05, reading
  `abort_multiparts` for the bucket delete; derived from the source, not
  reproduced.

## radosgw's retried bucket write can land in a bucket re-created under the same name

- **Kind:** defect, a race, unfixed at v19.2.6 and v20.2.4; not
  security-relevant: Low, triage estimate CVSS ≤3.1 (rgw-bug-reproduction).
  The window cannot be widened or steered by a requester, and the race is of
  the same by-name class as "radosgw's bucket delete unlinks a bucket
  re-created under the same name from its owner", above. Unreproduced
  upstream behaviour: derived from the source.
- **Evidence:** paths are under `src/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `retry_raced_bucket_write` runs its write and, while it fails with
    -ECANCELED, up to fifteen times calls `try_refresh_info` and the write
    again (`rgw/rgw_op.h:183-196`, `:197-210`). PutBucketPolicy,
    DeleteBucketPolicy, PutBucketTagging and DeleteBucketTagging write
    through it (`rgw/rgw_op.cc:8110-8115`, `:8204-8209`, `:1197-1201`,
    `:1232-1242`; `:9036-9046`, `:9151-9157`, `:1434-1438`, `:1469-1479`),
    as do other bucket subresource writes.
  - `try_refresh_info` reads the bucket into `s->bucket`'s info and attrs
    (`rgw/driver/rados/rgw_sal_rados.cc:780-783`, `:798-801`) through
    `try_refresh_bucket_info`, which clears the bucket id before it reads
    (`rgw/driver/rados/rgw_rados.cc:9011-9026`, the clear at `:9017`;
    `:9956-9971`, the clear at `:9962`), so `RGWBucketCtl::read_bucket_info`
    resolves the name through the entry point
    (`rgw/driver/rados/rgw_bucket.cc:3085-3105`, `:3234-3254`, the by-name
    branch at `:3096` and `:3245`), whatever instance it now names.
  - The version the request read is passed as `refresh_version`, which
    `read_bucket_instance_info` uses only to check the bucket-info cache
    against (`rgw/services/svc_bucket_sobj.cc:272-290` at v19.2.6, the
    check at `:284-290`; `:224-240` at v20.2.4), not to require the same
    instance.
  - A write to an instance that was removed fails with -ECANCELED: the
    system-object write recreates the object before its `cls_version`
    check (`rgw/services/svc_sys_obj_core.cc:496-506` at both tags), which
    reads version 0 from an object without the version attr and fails the
    `EQ` condition (`cls/version/cls_version.cc:55-66` and `:189-197` at
    both tags), so the whole op is refused.
  - So when the bucket is deleted and a bucket of the same name created,
    by anyone, between the request's load and its write, the retry writes
    into the new bucket. PutBucketPolicy's retry merges the attrs its
    request started with, the old bucket's ACL among them, into the new
    bucket ("radosgw's PutBucketPolicy retry writes back the bucket attrs
    its request started with", below); DeleteBucketPolicy and
    DeleteBucketTagging remove the new bucket's policy or tags.
- **Impact:** in that window a requester authorized on its own bucket can
  set the policy, and the ACL, of a bucket another owner just created under
  the name, or remove its policy or tags. The window is short, and needs
  the requester's bucket to be deleted and the name taken while its write
  is in flight, which the requester cannot arrange. The retry is not
  authorized again either ("radosgw's retried bucket writes are not
  authorized again", below), so it writes for a requester the new bucket's
  owner never allowed.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. `op.RetryRacedBucketWrite` reads the
  bucket by name, as radosgw does, and stops with 409 ConcurrentModification,
  writing nothing, when the name now names another instance
  (`internal/op/bucketsubres.go`; `docs/exclusions.md`, "A retried bucket
  write stops when the name names another bucket").
- **Upstream:** none. rgw-bug-reproduction's triage on 2026-10-07 found no
  report or fix; [#51572](https://tracker.ceph.com/issues/51572) reports a
  distinct defect of the same retry ("radosgw's PutBucketPolicy retry
  writes back the bucket attrs its request started with", below).
- **Found:** phase 1 unit M, Task 11, 2026-10-07, transcribing
  `retry_raced_bucket_write`; derived from the source, not reproduced.

## radosgw's PutBucketPolicy retry writes back the bucket attrs its request started with

- **Kind:** defect, a race, unfixed at v19.2.6 and v20.2.4;
  security-relevant: it can silently revert a concurrent change of a
  security control, the bucket's ACL or public-access block. Triage
  estimate CVSS ~5.0 (rgw-bug-reproduction). Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWPutBucketPolicy::execute` copies `s->bucket_attrs`, the attrs the
    request loaded, once, before its retried write (`rgw_op.cc:8102`,
    `:9028`), and each try sets the policy in that copy (`:8111-8112`,
    `:9037-9038`) and hands it to `merge_and_store_attrs` (`:8113`,
    `:9044`), inside the retry at `:8110-8115` and `:9036-9046`.
  - `merge_and_store_attrs` lays every attr of the copy over the bucket's
    own and stores the result under the bucket's version
    (`driver/rados/rgw_sal_rados.cc:771-778`, `:789-796`).
  - A try that lost to another write is retried after `try_refresh_info`
    has read that write's attrs into the bucket (`rgw_op.h:183-196`,
    `:197-210`). The copy still holds the values from before it, so the
    retry stores them over it: an ACL, tag set or public-access block
    another request changed in that time returns to its old value, while
    the 2xx of the request that changed it stands.
  - PutBucketPublicAccessBlock has the same stale snapshot: each try copies
    `s->bucket_attrs` afresh, but that is still the request-start set
    (`rgw_op.cc:8656-8660`, `:9637-9641`), so a policy write and a block
    write that race can each revert the other. PutBucketCors does the same
    (`:6100-6104` at v19.2.6, the copy at `:6771` at v20.2.4).
  - PutBucketTagging builds its copy from `s->bucket->get_attrs()` inside
    the retried write, so each try starts from the refreshed attrs
    (`rgw_op.cc:1197-1201`, `:1434-1438`).
- **Impact:** a PutBucketPolicy that races a PutBucketAcl making the bucket
  private, or a PutPublicAccessBlock changing an existing block, can restore
  the public ACL or the older block, with both requests told they succeeded;
  a PutPublicAccessBlock that races a PutBucketPolicy can likewise put back
  the older policy. The policy's own block check also ran against the block
  the request started with.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. Each try sets only the policy attr over
  the bucket as last read (`op.PutBucketPolicy`,
  `internal/op/bucketsubres.go`; `docs/exclusions.md`, "A retried
  PutBucketPolicy keeps what the write it lost to changed"). rgw-go does not
  serve PutBucketPublicAccessBlock or PutBucketCors yet.
- **Upstream:** [#51572](https://tracker.ceph.com/issues/51572),
  "retry_raced_bucket_write() callers not handling attrs correctly", open
  since 2021-07-07, reports the same mechanism and the same fix, building
  the attrs from the refreshed bucket inside the retried write; found as
  prior art by rgw-bug-reproduction's triage. No fix PR.
- **Found:** phase 1 unit M, Task 11, 2026-10-07, transcribing
  `RGWPutBucketPolicy::execute`; derived from the source, not reproduced;
  reported upstream before us.

## radosgw's PutBucketAcl answers success when its write loses a race

- **Kind:** quirk: intentional upstream behaviour. Single principal, triage
  estimate CVSS ~2.7 (rgw-bug-reproduction). Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWPutACLs::execute` (`rgw_op.cc:5821-5928`, `:6467-6590`), which
    serves PutBucketAcl and PutObjectAcl alike, stores the new ACL once,
    with no retry: at bucket scope with `merge_and_store_attrs` under the
    bucket version the request loaded (`:5920-5924`, `:6582-6586`), at
    object scope with `modify_obj_attrs` (`:5916-5919`, `:6578-6581`).
  - On -ECANCELED it sets `op_ret = 0`: "lost a race, but it's ok because
    acls are immutable" (`:5925-5927`, `:6587-6589`).
  - Those three lines are 6e9a915b565, "rgw: don't fail if lost race when
    setting acls" (2016-09-30, first in v11.0.1), whose message gives the
    intent: "Instead of retry, just return success (same effect as if we won
    and then other writer overwrote us)". `git blame` attributes the lines
    to it at both tags.
  - That rationale holds strictly only when the racing write also set an
    ACL. The -ECANCELED means some other write of the bucket instance, or
    of the object, landed first: a PutBucketTagging, a PutBucketPolicy, a
    reshard or a metadata write. Had the two been serialized, last writer
    wins would have kept the new ACL, so the drop reaches further than its
    justification.
- **Impact:** the ACL stays as it was before the request, so nothing becomes
  more exposed than before; an intended change, a tightening among them,
  silently does nothing while the client is told 200. S3 clients do not
  read the ACL back.
- **Releases:** every release since v11.0.1; checked at v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. `op.PutBucketACL` retries the write
  through `op.RetryRacedBucketWrite`, checks the owner and the block of
  public ACLs again against the bucket each try reads, and answers 409
  ConcurrentModification once the retries are spent
  (`internal/op/bucketsubres.go`; `docs/exclusions.md`, "A PutBucketAcl that
  loses a race is retried"). `op.PutObjectACL` does the same through
  `op.RetryRacedWriteReauthorized`, reading the object's head again and
  authorizing the requester against it before each retry
  (`internal/op/objectattrs.go`).
- **Upstream:** [#16930](https://tracker.ceph.com/issues/16930), fixed by
  6e9a915b565923081f609048072b8d75716a74ea, "rgw: don't fail if lost race
  when setting acls", which introduced this behaviour on purpose; found by
  rgw-bug-reproduction's prior-art search. No fix PR, as upstream intends it.
- **Found:** phase 1 unit M, Task 11, 2026-10-07, transcribing
  `RGWPutACLs::execute`; derived from the source, not reproduced; the
  behaviour is upstream's deliberate choice, so not a finding of ours.

## Tentacle never stores the confirmation of x-amz-confirm-remove-self-bucket-access

- **Kind:** defect, unfixed at v20.2.4; v19.2.6 has no such header or attr.
  Not security-relevant (triage estimate CVSS 0, rgw-bug-reproduction,
  2026-10-07): it fails safe, leaving the root the pass it had before the
  header existed. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`, at v20.2.4.
  - Put, Get and DeleteBucketPolicy let the root of the bucket owner's
    account through before any policy is evaluated, unless the bucket's
    attrs hold `RGW_ATTR_IAM_POLICY_REMOVE_SELF_ACCESS`
    (`rgw_op.cc:8983-8986`, `:9069-9072`, `:9126-9129`;
    `rgw_common.h:177`).
  - PutBucketPolicy records the confirmation by setting that attr to an
    empty value when the request carries
    `x-amz-confirm-remove-self-bucket-access` (`rgw_op.cc:9039-9041`).
  - The attrs reach RADOS through `rgw_put_system_obj`
    (`driver/rados/rgw_tools.cc:154-172`) and `RGWSI_SysObj_Core::write`,
    which skips every attr with an empty value
    (`services/svc_sys_obj_core.cc:518-526`). Only the caches hold the
    empty attr, until they drop the bucket.
  - Without the header the request erases the attr from its own copy only
    (`rgw_op.cc:9042`), which `merge_and_store_attrs` never removes from
    the bucket's attrs (`driver/rados/rgw_sal_rados.cc:789-796`).
- **Impact:** an account root that confirmed it may lose access through the
  policy it puts gets the root's pass back once the gateway's caches drop
  the bucket, and at once on any other gateway, so the confirmation does
  not hold.
- **Releases:** v20.2.4.
- **rgw-go:** not affected, as it serves neither the root's pass nor the
  header (`docs/exclusions.md`, "The account root's pass on the bucket
  policy ops"). Its DeleteBucketPolicy removes the attr on Tentacle, as
  radosgw's does.
- **Upstream:** none for this defect; rgw-bug-reproduction's triage found
  no report or fix. The header and its attr came with
  [ceph/ceph#57629](https://github.com/ceph/ceph/pull/57629) (merged
  2025-03-18, first in v20.1.0), whose commit names
  [#66177](https://tracker.ceph.com/issues/66177) as the issue it fixes.
- **Found:** phase 1 unit M, Task 11, 2026-10-07, reading Tentacle's
  `RGWPutBucketPolicy`; derived from the source, not reproduced.

## radosgw's PutBucketAcl and PutObjectAcl refuse a request without a Content-Length that they mean to accept

- **Kind:** defect, low; unfixed at v19.2.6 and v20.2.4: the upstream fix
  for this refusal is dead code. Not security-relevant: it fails closed
  (triage estimate CVSS 0, rgw-bug-reproduction, 2026-10-07). Unreproduced:
  derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWPutACLs_ObjStore_S3::get_params` returns 0 for
    `-ERR_LENGTH_REQUIRED` when `s->length` is set, under the comment "a
    request body is not required an S3 PutACLs request"
    (`rgw_rest_s3.cc:3624-3632`, `:3907-3915`). The guard is inverted: it
    tests `!!(s->length)` where the comment's case, no Content-Length,
    needs `!(s->length)` (`:3629-3630`, `:3912-3913`). It came in so with
    93117109f13, "rgw: s3: don't require a body in S3 put-object-acl".
  - `RGWOp::read_all_input` calls `rgw_rest_read_all_input` without its
    `allow_chunked` argument, so chunked input is always allowed
    (`rgw_op.h:215-227`, `:229-241`; the default, `rgw_op.h:127-129` at
    v19.2.6). `rgw_rest_read_all_input` then returns
    `-ERR_LENGTH_REQUIRED` only when `s->length` is null and the request is
    not chunked (`rgw_rest.cc:1566-1569`, `:1571-1574`), and `s->length` is
    null exactly when the request carried no Content-Length
    (`rgw_rest.cc:2203-2238` at v19.2.6), so the condition never holds.
- **Impact:** an ACL PUT that carries its ACL in `x-amz-acl` or the grant
  headers and sends neither a Content-Length nor a chunked body, as a
  client may for a request without a body, is refused 411
  MissingContentLength. The client works around it by sending
  `Content-Length: 0`.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** reproduces it for PutBucketAcl and PutObjectAcl
  (`readParamBody`, `internal/s3/bucket.go`), so both gateways answer such
  a request alike.
- **Upstream:** [#43148](https://tracker.ceph.com/issues/43148), still
  open, is the issue that
  [ceph/ceph#31987](https://github.com/ceph/ceph/pull/31987) (merged
  2020-02-04), "rgw: s3: don't require a body in S3 put-object-acl", set
  out to fix; that PR is the inverted guard above, so the refusal stands.
  Found by rgw-bug-reproduction's prior-art search; what is ours is that
  the merged fix never runs.
- **Found:** phase 1 unit M, Task 11, 2026-10-07, transcribing
  `RGWPutACLs_ObjStore_S3::get_params`; derived from the source, not
  reproduced.

## radosgw lets an upload id address another key's multipart upload

- **Kind:** defect, security-relevant (triage estimate CVSS 4.2-7.1):
  authorization is decided for one key while the op acts on another key's
  upload. Confirmed by rgw-bug-reproduction on 2026-10-07. Unreproduced:
  derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWMPObj::init` names the meta object `<key>.<upload id>.meta` and the
    part prefix `<key>.<upload id>` with no check of the id
    (`services/svc_tier_rados.h:42-55`, the names built at `:52-54`,
    unchanged at v20.2.4), and `RadosMultipartUpload` builds its `mp_obj`
    that way (`driver/rados/rgw_sal_rados.h:801`, `:856`).
  - Nothing else checks it: `RGWMPObj::from_meta` parses a meta name from
    its end, `<key>` being all before the second-last "." (`:76-87`), and
    `is_v2_upload_id` checks only the `2~` or `2/` prefix
    (`rgw_multi.cc:76-82`, both tags).
  - UploadPart, CompleteMultipartUpload, AbortMultipartUpload and ListParts
    take the upload id from the query unchecked and pass it with the
    request's key to `get_multipart_upload` (`rgw_op.cc:4246`, `:6416`,
    `:6625`, `:6675`; `:4455`, `:7211`, `:7549`, `:7599`); ListParts'
    policy read does the same (`:414`; `:444`).
  - Each op authorizes the request's key: `verify_bucket_permission` with
    `ARN(s->object->get_obj())`, or `verify_object_permission` on
    `s->object` (`:3982-3985`, `:6354-6357`, `:6601-6604`, `:6658`;
    `:4191-4194`, `:7030-7033`, `:7525-7528`, `:7582`).
  - So key `a` with upload id `b.2~X` names exactly key `a.b`'s upload
    `2~X`, and the request is authorized as key `a`. No gateway makes an id
    with a ".": radosgw's is `2~` and gen_rand_alphanumeric's
    `A-Za-z0-9-_` (`driver/rados/rgw_sal_rados.cc:3281-3283`, `:4125-4127`;
    `common/random_string.cc:48` at both tags).
- **Impact:** the request is doubly gated: it needs the victim upload's
  id, a secret that ListMultipartUploads gives only to a principal allowed
  to list the uploads, and a policy that allows key `a` but denies key `a.b`,
  whether an exact key or a pattern. Such a principal can list `a.b`'s
  parts and replace them, so read and tamper, and abort the upload.
  CompleteMultipartUpload writes the object to the authorized request key,
  `s->object` (`rgw_op.cc:6483-6485`; `:7367-7369`), not the aliased one,
  so it assembles the victim's parts into the principal's own object `a`
  and removes the victim's meta object: the content is taken and the
  victim's upload is gone.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. UploadPart, UploadPartCopy,
  CompleteMultipartUpload, AbortMultipartUpload and ListParts answer an
  upload id holding a "." as a missing upload without reading the store
  (`aliasedUpload`, `internal/op/uploadpart.go`), and the driver's
  meta-object reference refuses one too (`metaRef`,
  `internal/driver/mp_layout.go`; `docs/exclusions.md`, "An upload id
  holding a "." is NoSuchUpload").
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-07; awaiting response. The fix would check that an upload id is
  well formed at the four handlers before it names an upload.
- **Found:** phase 1 unit P, Task 8 review, 2026-10-07, reading how the
  multipart ops name their upload; derived from the source, not reproduced.

## radosgw holds a copy-source range's bounds in an off_t, so a bound past 2^63 reads as a suffix

- **Kind:** defect, security-relevant: resource exhaustion (triage
  estimate CVSS 7.7, CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:C/C:N/I:N/A:H; 6.5 with
  S:U). rgw-bug-reproduction's first triage called it a quirk; its
  re-triage of radosgw's copy loop confirmed the defect. Unreproduced here:
  derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - RGWPutObj holds `x-amz-copy-source-range`'s bounds in `off_t`s
    (`rgw_op.h:1227-1228`; `:1297-1298`) and fills them with `strtoull`
    (`rgw_op.cc:3893-3894`; `:4102-4103`), so a bound of 2^63 or more turns
    negative, and one past 2^64 saturates and turns -1.
  - The only check is that the first bound is not past the last as `off_t`
    compares (`:3895-3899`; `:4104-4108`), which a negative first bound
    passes unless the last is lower still.
  - `range_to_ofs` reads a negative offset as a suffix from the object's
    end, clamped to its first byte, with its last byte as the end
    (`rgw_sal.cc:429-448`; `:426-445`).
  - `RGWPutObj::execute` copies the range in a loop that asks `get_data`
    for each piece from `fst` and advances `fst` by the bytes read
    (`rgw_op.cc:4386-4421`, the advance at `:4399`; `:4618-4653`, `:4631`).
    With `fst` far below zero each piece reads the whole source, so `fst`
    climbs by the source's size per pass and stays negative for about
    2^63 divided by that size passes, each writing the source into the
    part.
  - Nothing bounds the loop: the quota is checked before it, against a
    copy's content length of 0 (`:4202`; `:4411`), and after it (`:4444`;
    `:4676`), which the loop does not reach, and no `rgw_max_put_size` or
    part size check applies.
- **Impact:** resource exhaustion, as rgw-bug-reproduction's re-triage
  confirms: one UploadPartCopy goes on reading the source and writing it
  into the part for about 2^63 bytes, unchecked by any quota, tying up its
  request and the cluster's capacity. The requester needs s3:PutObject on a
  destination and s3:GetObject on a source.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it: a first bound of 2^63 or more is 416
  InvalidRange before the op authorizes (`ParseCopySourceRange`,
  `internal/op/multipartxml.go`; `docs/exclusions.md`, "A copy-source range
  past 2^63 answers 416").
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-07; awaiting response. rgw-bug-reproduction's prior-art search
  names [ceph/ceph#32487](https://github.com/ceph/ceph/pull/32487), which
  covers only the parse's digit check, and relates it to
  [#71458](https://tracker.ceph.com/issues/71458), CVE-2025-48052, an
  UploadPartCopy crash on a copy source's url escape: the same op, a
  distinct defect. The fix would refuse a negative or out-of-range bound
  with 416 in `init_processing`, before the loop, as rgw-go does.
- **Found:** phase 1 unit P, Task 8, 2026-10-07, transcribing
  init_processing's range parse; derived from the source, not reproduced.

## RGWOp::read_all_input ignores its allow_chunked argument

- **Kind:** defect, latent and benign (triage estimate CVSS 0): the
  callers' argument has no effect, a conformance defect. Confirmed by
  rgw-bug-reproduction on 2026-10-07. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`.
  - `RGWOp::read_all_input(s, max_len, allow_chunked)` calls
    `rgw_rest_read_all_input(s, max_len)` without its third argument, whose
    default is true (`rgw_op.h:215-227` and `:127-129` at v19.2.6,
    `:229-241` at v20.2.4).
  - The callers that pass false, among them the ACL, lifecycle, object
    lock, legal hold and DeleteObjects bodies (`rgw_rest.cc:1474`, `:1482`,
    `:1489`, `:1496`, `:1672` at v19.2.6; `:1479`, `:1487`, `:1494`,
    `:1501`, `:1677` at v20.2.4), therefore read a chunked body, up to
    `rgw_max_put_param_size` as `read_all_chunked_input` bounds it, where
    they ask for 411 MissingContentLength.
- **Impact:** none known: those ops accept a chunked request their code
  means to refuse, and read it within the same bound, which
  `read_all_chunked_input` still enforces (`rgw_rest.cc:1501-1535`, the
  check at `:1525`, at v19.2.6; `:1506-1540`, `:1530`, at v20.2.4). A
  routine upstream cleanup.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** reproduces it on purpose: `op.ReadParamBody` reads a chunked
  body for CompleteMultipartUpload and DeleteObjects as radosgw does
  (`internal/op/parambody.go`).
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix; [#81230](https://tracker.ceph.com/issues/81230), on the
  same function's payload hash, is unrelated. The fix is a cleanup that
  forwards `allow_chunked`.
- **Found:** phase 1 unit P, Task 8, 2026-10-07, transcribing
  CompleteMultipartUpload's body read; derived from the source, not
  reproduced.

## Removing an account's root user leaves its name in the account's users index

- **Kind:** defect, unfixed at v19.2.6 and v20.2.4: a leaked index entry.
  Not security-relevant (triage estimate CVSS 0, rgw-bug-reproduction,
  2026-10-07). Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/services/`; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `account_users_link` decides when a user write links the user into
    its account's `users.<account id>` index, and names every user with
    an account, the root user included (`svc_user_rados.cc:166-171`;
    `:148-153`). So creating an account's root user adds its display name
    to the index (`PutOperation::complete`, `:360-375`; `:340-355`).
  - `remove_user_info` unlinks the name only `else if (info.type !=
    TYPE_ROOT)` (`:601-610`; `:575-584`), so removing the root user leaves
    its entry behind.
  - The two disagree since ceph commit b6033599726 ("rgw: link account
    root to account user index"; on Squid its cherry-pick 442ea928483), which
    dropped the `info->type != TYPE_ROOT` condition from
    `account_users_link` so that root users would hold off `account rm`,
    and left the one in `remove_user_info`, which the first account-index
    commit had added with it.
- **Impact:** after `radosgw-admin user rm` or the admin API's
  `DELETE /admin/user` of an account's root user, the entry names a user
  that is gone, and the durable effect is that the name stays blocked:
  - `PutOperation::prepare` reads the entry before any add and refuses
    with EEXIST when it names another uid (`:272-290`; `:251-269`), so no
    other user of the account can take the name (409 UserAlreadyExists
    from the admin API's create, BucketAlreadyExists from its modify);
    only a user with the root's uid overwrites the entry.
  - `account rm` is not held off: it refuses only while
    `list_account_users` returns users (`rgw_account.cc:316-319` at both
    tags), and `list_account_users` drops an id whose user does not load
    (`driver/rados/rgw_sal_rados.cc:1420-1429` at v19.2.6, `:1959-1968` at
    v20.2.4).
  - The entry still counts in the index header, which cls_user compares
    with the limit callers pass to `account_resource_add`
    (`src/cls/user/cls_user.cc:545-557` at v19.2.6), so it takes one of
    the account's user slots.
  - Tracker #81353, #81348 and #81347
    ([#81353](https://tracker.ceph.com/issues/81353),
    [#81348](https://tracker.ceph.com/issues/81348),
    [#81347](https://tracker.ceph.com/issues/81347)) report distinct
    sibling defects, not this one.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. Removing an account user, the root
  included, removes its index entry, and a user whose entry is already
  gone is removed without error (`RemoveUser`, `internal/op/adminuser.go`;
  `docs/exclusions.md`, "The admin user routes answer store failures, and
  keep the account users index clean where radosgw leaves entries
  behind").
- **Upstream:** none. rgw-bug-reproduction's prior-art search found no
  report or fix; the three trackers above report distinct siblings. The
  fix is to unlink the root user's name too.
- **Found:** phase 1 unit N, Task 4, 2026-10-07, reading
  `remove_user_info` for the admin API's user removal; derived from the
  source, not reproduced.

## radosgw cannot remove an account user whose users index entry is gone

- **Kind:** defect, unfixed at v19.2.6 and v20.2.4: a user that can no
  longer be removed. Not security-relevant (rgw-bug-reproduction,
  2026-10-07): it fails closed, the admin's removal answering an error.
  The lost race taken as success, below, is upstream's intent, not this
  defect. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `remove_user_info` removes the key, Swift and email indexes and the
    group links, each tolerating ENOENT, but unlinks a non-root account
    user from its account's users index without that tolerance
    (`services/svc_user_rados.cc:601-610`; `:575-584`), before it removes
    the uid object.
  - The uid object's removal is checked against the version the removal
    read (`RadosUser::remove_user`, `driver/rados/rgw_sal_rados.cc:278-282`;
    `:295-299`), and a lost race, ECANCELED, is taken as success with the
    object left (`remove_uid_index`, `services/svc_user_rados.cc:639`;
    `:614-616`). A modify of the user while `user rm --purge-data` purges
    its buckets is such a race. That success is deliberate: e0283704abc8
    ("rgw: svc.user_rados: split svc.user", 2019) carries it, and
    a2b37a10f80c (2023, Tentacle only) comments the return "success but no
    mdlog entry". The race needs a concurrent privileged writer, as there
    is no self-service modify of a user (triage estimate CVSS 2.6 for it).
  - So a removal that loses that race, or fails on the uid object
    otherwise, leaves the user object with its key, Swift and email indexes
    and its account entry already removed, unless the write that won the
    race linked them again, and every later removal fails at the unlink
    with ENOENT, which `RGWUserAdminOp_User::remove` answers as NoSuchUser
    (`driver/rados/rgw_user.cc:2488-2492`; `:2494-2498`).
- **Impact:** the user object stays, readable by uid, and neither the
  admin API nor `radosgw-admin user rm` can remove it; after a lost race
  radosgw also reported the first removal as a success, as it means to
  ("radosgw's user removal reports success over a lost version race,
  leaving the user without its indexes", above).
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. It takes a missing entry as removed
  and answers a lost race 409 ConcurrentModification, so a retry removes
  the user (`RemoveUser`, `internal/op/adminuser.go`; `docs/exclusions.md`,
  "The admin user routes answer store failures, and keep the account
  users index clean where radosgw leaves entries behind").
- **Upstream:** none. rgw-bug-reproduction's triage found no report or fix
  of the unlink's missing ENOENT tolerance. The fix would tolerate ENOENT
  there, as the group unlinks do.
- **Found:** review of phase 1 unit N, Task 4, 2026-10-07, reading
  `remove_user_info` for the user removal's retry; derived from the
  source, not reproduced.

## radosgw's admin user info shows the Swift TempURL keys to a caller it withholds keys from

- **Kind:** defect, security-relevant (a cross-user secret disclosure),
  unfixed at v19.2.6 and v20.2.4 and on ceph main at 7ed73efc1be.
  Confirmed by rgw-bug-reproduction on 2026-10-07; its triage estimate is
  CVSS 8.1 (CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:H/I:H/A:N), within a range
  of 6.4 to 8.1.
- **Evidence:** each pair of lines is v19.2.6's, then v20.2.4's.
  - `user-info-without-keys` is a cap of its own, grantable without
    `users` (`RGWUserCaps::is_valid_cap_type`, `rgw_common.cc:2099`;
    `:2162`). `GET /admin/user` admits a caller holding only
    `user-info-without-keys=read` (`RGWOp_User_Info::check_caps`,
    `driver/rados/rgw_rest_user.cc:77-83`; the same lines), and sets
    `dump_keys` only for a caller holding `users=read`, a system request or
    an admin (`:124-127`; the same lines, `is_admin_of(uid)` at v19.2.6 and
    `is_admin()` at v20.2.4). The admin ops documentation promises such a
    caller no keys (`doc/radosgw/adminops.rst:276-278` at v19.2.6).
  - `dump_user_info` withholds `keys` and `swift_keys` behind
    `if (dump_keys)` (`driver/rados/rgw_user.cc:145-148`; `:150-153`) but
    encodes `temp_url_keys` unconditionally (`:162`; `:167`).
  - Those values are the user's Swift TempURL signing keys: the TempURL
    engine computes the signature over the request's own method, path and
    the URL's expiry with each of the owner's `temp_url_keys`, and grants
    the request on a match (`rgw_swift_auth.cc:366-369`, `:394-406` and
    `:416-436` at v19.2.6; `:367-370`, `:395-407` and `:417-437` at
    v20.2.4).
- **Impact:** a caller trusted to read users without their keys reads
  every user's TempURL keys. With them it can forge, offline and at no
  cost, a TempURL for any method, any of the victim's objects and any
  expiry, which any gateway serving the Swift API honors without further
  authentication. Only users with TempURL keys set, through Swift account
  metadata or `radosgw-admin user modify --temp-url-key`, are exposed.
  The fix is to move the `temp_url_keys` emit inside `if (dump_keys)`.
- **Releases:** v19.2.6 and v20.2.4; ceph main still encodes
  `temp_url_keys` outside the `dump_keys` block
  (`driver/rados/rgw_user.cc:142` at 7ed73efc1be).
- **rgw-go:** does not reproduce it. Its user document omits
  `temp_url_keys`, as it omits `keys` and `swift_keys`, when the caller may
  not see keys (`dumpUserInfo`, `internal/admin/user.go`;
  `docs/exclusions.md`, "The admin user document withholds the Swift
  TempURL keys with the other keys").
- **Upstream:** reported to the Ceph security team (security@ceph.io) on
  2026-10-07; awaiting response. rgw-bug-reproduction's prior-art search
  found no report or fix.
- **Found:** phase 1 unit N, Task 4, 2026-10-07, in a security review of
  the admin user document; derived from the source, not reproduced.

## A stale bucket list entry blocks a user's removal for good

- **Kind:** defect, unfixed at v19.2.6 and v20.2.4: a user that can no
  longer be removed. Not security-relevant (triage estimate CVSS 0,
  rgw-bug-reproduction, 2026-10-07): it fails closed. Unreproduced: derived
  from the source.
- **Evidence:** paths are under `src/rgw/driver/rados/`; each pair of lines
  is v19.2.6's, then v20.2.4's.
  - `RadosBucket::remove` deletes the bucket (`rgw_sal_rados.cc:445`;
    `:462`) before it unlinks it from its owner's bucket list (`:461`;
    `:482`), so a gateway that stops between the two leaves the list
    naming a bucket that is gone. An unlink that fails does the same: its
    error is logged and returned (`:461-467`; `:482-488`), after the
    bucket is gone.
  - `execute_remove` loads every bucket the list names and returns the
    load's ENOENT (`rgw_user.cc:1968-1974`; `:1975-1980`), which
    `RGWUserAdminOp_User::remove` answers as NoSuchUser (`:2488-2492`;
    `:2494-2498`); without purge-data the entry refuses the removal with
    EEXIST before any load (`:1964-1967`; `:1970-1973`). The entry stays,
    so every retry answers the same.
- **Impact:** the owner can no longer be removed through the admin API or
  `radosgw-admin user rm`, which answer NoSuchUser for a user that
  exists, until the list entry is removed by hand. `radosgw-admin bucket
  unlink` does not remove it: `RGWBucketAdminOp::unlink` loads the bucket
  by name before it unlinks and answers the load's error
  (`rgw_bucket.cc:1017-1023` and `:195-200`; `:1168-1174` and `:196-201`),
  and the delete removed the bucket's entry point
  (`rgw_rados.cc:5279-5285`; `:5993-5999`); the command passes no bucket
  id to the unlink (`src/rgw/rgw_admin.cc:7426-7432` at v19.2.6,
  `src/rgw/radosgw-admin/radosgw-admin.cc:7673-7679` at v20.2.4).
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** reproduces it. Its bucket delete unlinks the list entry last
  too (`DeleteBucket`, `internal/driver/bucketops.go`), and its user
  removal answers a listed bucket that is gone with NoSuchUser
  (`RemoveUser`, `internal/op/adminuser.go`).
- **Upstream:** none. rgw-bug-reproduction's triage found no report or
  fix. The fix would unlink the bucket before it deletes it, or let the
  user removal take a listed bucket that is gone as removed.
- **Found:** re-review of phase 1 unit N, Task 4, 2026-10-07, reading the
  user removal's bucket loop; derived from the source, not reproduced.

## radosgw stores request credentials as object attrs

- **Kind:** defect, informational: a defense-in-depth gap, not
  security-relevant across principals. rgw-bug-reproduction's triage found
  the attrs stored but readable by no principal through S3. Unreproduced:
  derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `req_info::init_meta_info` files every header whose name starts with
    `x-amz-`, and the other meta prefixes, into `x_meta_map`
    (`rgw_common.cc:413-464`; `:426-477`).
  - `rgw_get_request_metadata` stores every entry its blocklist does not
    name as the attr `user.rgw.` and the name (`rgw_op.h:2171-2233`;
    `:2338-2408`). v19.2.6's blocklist names the three SSE-C headers and
    `x-amz-storage-class` (`:2177-2182`); v20.2.4's adds the three
    `x-amz-copy-source-server-side-encryption-customer-*` headers,
    `x-amz-content-sha256`, `x-amz-checksum-algorithm` and `x-amz-date`
    (`:2344-2357`). Neither names `x-amz-security-token`.
  - PutObject stores the attrs it builds so (`rgw_op.cc:4525-4529`;
    `:4793-4797`), and so does CopyObject (`init_common`, `rgw_op.cc:5523`;
    `:6089`), whose `copy_obj` keeps the request's attrs under
    `x-amz-metadata-directive: REPLACE` (`set_copy_attrs`,
    `driver/rados/rgw_rados.cc:3673-3700` and `:4787`; `:3858-3885` and
    `:5043`).
  - So both releases store a request's STS session token,
    `x-amz-security-token`, as `user.rgw.x-amz-security-token`, and a Squid
    CopyObject with REPLACE stores its
    `x-amz-copy-source-server-side-encryption-customer-key`, the customer
    key of an SSE-C source, as an attr of the copy. Squid refuses an
    encrypted source before it writes (`driver/rados/rgw_rados.cc:4755-4763`
    at v19.2.6), so the key is stored when the source is not encrypted.
  - No S3 read returns those attrs: GET and HEAD render, among the user
    attrs, only those under `user.rgw.x-amz-meta-` (`rgw_rest_s3.cc:581-585`;
    `:698-702`); v20.2.4's GetObjectAttributes renders named fields alone
    (`RGWGetObjAttrs_ObjStore_S3::send_response`, `rgw_rest_s3.cc:3999-4137`
    at v20.2.4; Squid has no such op); and a notification's metadata keeps
    only `x-amz-meta-` names (`filter_amz_meta` and
    `metadata_from_attributes`, `driver/rados/rgw_notify.cc:922-948`;
    `:936-962`).
- **Impact:** the token, and on Squid such a customer key, sit in the
  clear in the object's xattrs, where only a reader of the data pool or of
  `radosgw-admin object stat` sees them; no S3 principal reads them.
- **Releases:** v19.2.6 for the copy-source key; v19.2.6 and v20.2.4 for
  the session token.
- **rgw-go:** does not reproduce it: it applies v20.2.4's blocklist on both
  releases and adds `x-amz-security-token` to it (`requestAttrs`,
  `internal/s3/requestattrs.go`; `docs/exclusions.md`, "Request headers
  stored as attrs follow Tentacle's blocklist, and the session token is
  never stored").
- **Upstream:** [#65460](https://tracker.ceph.com/issues/65460) for the
  session token. The copy-source SSE-C key left the stored attrs with
  [ceph/ceph#63794](https://github.com/ceph/ceph/pull/63794) on main and its
  Tentacle backport [ceph/ceph#69277](https://github.com/ceph/ceph/pull/69277);
  it has no Squid backport. Found by rgw-bug-reproduction's prior-art
  search.
- **Found:** phase 1 unit W, Task 11, 2026-10-07, transcribing
  `rgw_get_request_metadata` at both tags; derived from the source, not
  reproduced; already known upstream, so not a finding of ours.

## radosgw's CopyObject stores an object lock on a bucket without object lock

- **Kind:** defect, not security-relevant (rgw-bug-reproduction's triage
  estimate CVSS 0). Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - PutObject and CreateMultipartUpload refuse a retention or a legal hold
    on a bucket without object lock with -ERR_INVALID_REQUEST
    (`rgw_rest_s3.cc:2669-2673` and `:4021`; `:2830-2834` and `:4502`).
  - CopyObject's `get_params` parses the same three headers and makes no
    `obj_lock_enabled()` check (`rgw_rest_s3.cc:3478-3509`; `:3758-3789`),
    its `execute` adds the retention and the legal hold to the copy's attrs
    unconditionally (`rgw_op.cc:5584-5593`; `:6150-6159`), and `copy_obj`
    puts them over the source's under either metadata directive
    (`driver/rados/rgw_rados.cc:4768-4775`; `:5013-5020`).
  - The stray attrs are inert while the bucket has no object lock. A
    delete checks retention and legal hold only when `check_obj_lock =
    have_instance() && obj_lock_enabled()` (DeleteObject `rgw_op.cc:5186`,
    DeleteObjects `:6845`; `:5576`, `:7782`), so the copy stays deletable;
    GetObjectRetention and GetObjectLegalHold answer -ERR_INVALID_REQUEST,
    "bucket object lock not configured", before they read the attr
    (`rgw_op.cc:8443-8447` and `:8547-8551`; `:9404-9408` and
    `:9528-9532`).
  - They go live only if the owner later enables object lock, which
    PutObjectLockConfiguration does on a bucket whose versioning is enabled
    (`rgw_op.cc:8276-8286`; `:9224-9234`).
- **Impact:** a CopyObject that asks for a retention or a legal hold on a
  bucket without object lock succeeds where the same request as a
  PutObject is refused, and stores an attr nothing honours until the
  bucket's owner enables object lock, a change the owner makes. No
  principal gains anything across a boundary.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it: CopyObject refuses such a request with
  400 InvalidRequest, as PutObject does (`objectLock`,
  `internal/s3/putobject.go`; `docs/exclusions.md`, "CopyObject refuses an
  object lock on a bucket without object lock").
- **Upstream:** none. rgw-bug-reproduction's triage on 2026-10-07 found no
  report or fix. The fix would gate CopyObject's object-lock headers on
  `obj_lock_enabled()` in its `get_params`, as PutObject's are.
- **Found:** phase 1 unit W, Task 11, 2026-10-07, comparing PutObject's and
  CopyObject's `get_params`; derived from the source, not reproduced.

## radosgw's Swift key modify rebuilds the key

- **Kind:** defect, security-relevant: a revoked Swift credential comes
  back, and a Swift key can be stored with an empty secret that an empty
  `X-Auth-Key` matches. Unfixed at v19.2.6 and v20.2.4 and on ceph main.
  rgw-bug-reproduction confirmed it on 2026-10-07; its triage estimates
  are CVSS 6.5 for the revival and about 6.5 (9.1 in theory) for the
  empty secret.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is
  v19.2.6's, then v20.2.4's, and the code is the same at both.
  - `RGWAccessKeyPool::modify_key` copies an S3 key it modifies from the
    user's map, but for a Swift key starts from a fresh `RGWAccessKey`
    holding only the id and the subuser
    (`driver/rados/rgw_user.cc:673-683`; `:678-688`), whose members default
    to an empty secret, `active = true` and no creation date
    (`rgw_acl_types.h:44-49`; the same lines).
  - It then sets the secret only when one is named or generated, skipping
    an empty one (`rgw_user.cc:685-695`; `:690-700`), and the active flag
    only when the request names one (`:696-698`; `:701-703`), which only
    `RGWOp_Key_Create` can do (`driver/rados/rgw_rest_user.cc:709`;
    `:719`).
  - `modify_key` runs whenever a key op meets a Swift key the user holds:
    `PUT /admin/user?key` naming the subuser; `POST /admin/user?subuser`
    with `secret-key` or `generate-secret`; and `PUT /admin/user?subuser`
    for a subuser that exists, which always adds a key
    (`rgw_user.cc:1019-1025` and `:1074-1076`; `:1024-1030` and
    `:1079-1081`).
  - A Swift key is reachable for authentication through its `users.swift`
    index, which `PutOperation::complete` writes only for an active key
    and `remove_old_indexes` removes when a key stops being active
    (`services/svc_user_rados.cc:341-352` and `:434-442`; `:321-332` and
    `:414-422`).
  - An empty secret cannot be created: `generate_key` refuses one
    (`rgw_user.cc:589-592`; `:594-597`) and a subuser create generates one
    when none is named (`:1074-1076`; `:1079-1081`). It can be stored only
    through this modify path.
  - `RGW_SWIFT_Auth_Get::execute` refuses a request only when the
    `X-Auth-Key` header is absent (`rgw_swift_auth.cc:763`; `:764`), then
    compares the stored secret with it (`:781`; `:782`) with no guard for
    an empty one, so a present but empty `X-Auth-Key` matches an empty
    secret.
  - The Swift branch was left as it was when ceph commit 463f463d50943c6f
    ("rgw/user: add 'active' flag to RGWAccessKey", tracker #59186; on
    Squid its cherry-pick d3ee2d34fb4) added the active flag.
- **Impact:** a Swift key changed through the admin API or radosgw-admin's
  equivalents:
  - comes back active. A Swift key deactivated with `PUT
    /admin/user?key&active=false` is re-indexed, so rotating its secret
    through the subuser routes, or creating the subuser again, makes the
    revoked credential usable again; it also loses its creation date.
  - is left with an empty secret when `PUT /admin/user?key` names the
    subuser with `generate-key=false` and no `secret-key`. Storing it needs
    a `users=write` caller; once stored, anyone who knows
    `<uid>:<subuser>` authenticates to the Swift API with an empty
    `X-Auth-Key`.
  - The upstream fix is to seed `modify_key` from the stored Swift key,
    as its S3 branch does, and to refuse an empty key in Swift
    authentication.
- **Releases:** v19.2.6 and v20.2.4; ceph main is affected too.
- **rgw-go:** not affected. It changes a key in place, keeping a
  deactivated key deactivated and its creation date, and refuses any
  change that would leave a key without a secret (`modifyKey`,
  `internal/op/adminuser_sub.go`; `docs/exclusions.md`, "The admin key,
  subuser, caps and quota routes differ from radosgw in seven ways"). It
  serves no Swift authentication in phase 1 (`docs/exclusions.md`, "Swift
  API and Swift authentication"); a later phase that adds it must refuse
  an empty key.
- **Upstream:** pending: rgw-bug-reproduction classified it on 2026-10-07.
- **Found:** phase 1 unit N, Task 5, 2026-10-07, transcribing
  `RGWAccessKeyPool` for the admin key and subuser routes; derived from
  the source, and confirmed by rgw-bug-reproduction.

## radosgw's quota set stores garbage for an unparsable max-size-kb

- **Kind:** defect, non-security and fail-open: input radosgw cannot
  read, or a size that overflows, sets an unintended or unlimited quota
  and answers 200. Unfixed at v19.2.6 and v20.2.4. rgw-bug-reproduction
  triaged it on 2026-10-07 (CVSS 0: reachable only through the
  `users=write` admin quota route, crossing no privilege boundary).
- **Evidence:** paths are under `src/rgw/`; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `RGWOp_Quota_Set::execute`, setting one quota from the arguments,
    reads them with `RESTArgs::get_int64` and `get_bool` and checks no
    return (`driver/rados/rgw_rest_user.cc:1103-1111`; `:1113-1121`).
  - `get_int64` sets `*existed` and then hands the argument to
    `stringtoll` (`rgw_rest.cc:894-913`; `:899-918`), which returns EINVAL
    without writing its output for trailing text or a value that
    saturates at LLONG_MAX (`rgw_string.h:38-52` at both tags).
  - `max_size_kb` is a local declared without a value (`:1105`; `:1115`),
    and `has_max_size_kb` is set because the argument exists, so an
    unparsable `max-size-kb` multiplies an uninitialized value by 1024
    into the stored `max_size` (`:1108-1110`; `:1118-1120`).
  - An unparsable `max-objects` or `max-size` leaves the field the
    `RGWQuotaInfo()` constructor gave it, -1, unlimited
    (`rgw_quota_types.h:38-43` at both tags), and the request succeeds.
  - The same product, `max_size_kb * 1024`, has no overflow guard on the
    argument path (`rgw_rest_user.cc:1109`; `:1119`) nor in
    `RGWQuotaInfo::decode_json` for a JSON body (`rgw_quota.cc:1061`;
    `:1059`). A `max-size-kb` of 2^53 or more is signed overflow, undefined
    behaviour, which in practice wraps, 2^53 to INT64_MIN; a negative
    `max_size` is enforced as no limit (`rgw_quota.cc:775` and `:820`;
    `:796` and `:841`).
- **Impact:** `PUT /admin/user?quota&quota-type=user&max-size-kb=1.5`, or
  any value with a unit such as `10G`, stores an arbitrary maximum size,
  which can block every write to the user's buckets once the quota is
  enabled; `max-objects=10k` or `max-size=1G` silently removes the limit,
  and so does a `max-size-kb` of 2^53. Each answers 200.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** answers 400 InvalidArgument to an unparsable value and to an
  overflowing one, on the arguments and in a body, and stores nothing
  (`setQuotaInfo`, `internal/admin/user_sub.go`; `SetUserQuota`,
  `internal/op/adminuser_sub.go`; `Quota.UnmarshalJSON`,
  `internal/meta/quota_json.go`; `docs/exclusions.md`, "The admin key,
  subuser, caps and quota routes differ from radosgw in seven ways").
- **Upstream:** pending: rgw-bug-reproduction classified it on 2026-10-07
  (non-security, fail-open); no prior art.
- **Found:** phase 1 unit N, Task 5, 2026-10-07, transcribing the quota
  set's argument handling; the overflow in the task's review; derived
  from the source, not reproduced.

## radosgw's admin API takes an unparsable boolean argument as its default

- **Kind:** defect, non-security: informational hardening on admin-only
  routes. Unfixed at v19.2.6 and v20.2.4. Unreproduced: derived from the
  source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is
  v19.2.6's, then v20.2.4's.
  - `RESTArgs::get_bool` sets `*existed` when the argument is present. It
    takes an empty value, `true` in any case or `1` as true and `false` in
    any case or `0` as false; for anything else it writes the caller's
    default and returns -EINVAL (`rgw_rest.cc:1002-1032`; `:1007-1037`).
  - The admin user bodies call it as bare statements and discard that
    return (`driver/rados/rgw_rest_user.cc:183-188`, `:330-336`, `:464`,
    `:521-522`, `:592`, `:649`, `:701-702` and `:1111`; `:184-189`,
    `:336-342`, `:474`, `:531-532`, `:602`, `:659`, `:711-712` and
    `:1121`).
  - `RGWOp_Key_Create` defaults `active` to true and sets
    `access_key_active` whenever the argument exists (`:702` and
    `:708-710`; `:712` and `:718-720`). So `active=flase` makes the key
    active, the only flag whose typo fails open on a credential; it
    overlaps "radosgw's Swift key modify rebuilds the key". An empty
    `active=` is true too.
  - `generate-key` on user and key create and `purge-keys` default to
    true, so a typo there generates a key or purges keys, which is mild;
    on user modify `generate-key` defaults to false (`:330`; `:336`). `suspended`, `system`,
    `exclusive`, `purge-data` and `account-root` default to false. A typo
    in `suspended=true` leaves the user unsuspended, and on a user modify,
    which applies `suspended` whenever it exists (`:385-386`; `:392-393`),
    lifts a suspension the user already has.
- **Impact:** an operator's typo in an `active=false` revocation leaves
  the key active, and the 200 hides it. Admin-only: every route needs
  `users=write`.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. Those routes answer 400
  InvalidArgument for a boolean argument they cannot parse, after the cap
  check, and store nothing, on every flag, which is stricter than radosgw
  in both directions; an empty `active=` is refused the same way rather
  than read as true (`flags` and `runChecked`, `internal/admin/user.go`;
  `createKey`, `internal/admin/user_sub.go`; `docs/exclusions.md`, "The
  admin user routes refuse a boolean argument they cannot parse").
- **Upstream:** pending: sent to rgw-bug-reproduction.
- **Found:** review of phase 1 unit N, Task 5, 2026-10-07, reading the key
  create's argument handling; derived from the source, not reproduced.

## radosgw lets another user take an inactive access key's id

- **Kind:** defect, non-security (rgw-bug-reproduction: BENIGN), unfixed
  at v19.2.6 and v20.2.4: a key id whose holder cannot reactivate it.
  Unreproduced: derived from the source.
- **Evidence:** each pair of lines is v19.2.6's, then v20.2.4's.
  - An access key's `users.keys` index object exists only while the key
    is active: `PutOperation::complete` writes it for active keys
    (`src/rgw/services/svc_user_rados.cc:329-339`; `:309-319`) and
    `remove_old_indexes` removes it when the key stops being active
    (`:424-432`; `:404-412`).
  - Both duplicate checks read that index: `generate_key`'s
    (`src/rgw/driver/rados/rgw_user.cc:572-575`; `:577-580`) and
    `PutOperation::prepare`'s (`src/rgw/services/svc_user_rados.cc:257-270`;
    `:236-249`).
  - So another user can create a key with a deactivated key's id; the
    index then names that user, and the first holder's reactivation fails
    `prepare` with EEXIST.
- **Impact:** no impersonation and no secret exposure. A different user
  can create a key with a deactivated key's id, with its own secret. S3
  authentication resolves the id through the index to that one user and
  verifies that user's secret (`src/rgw/rgw_rest_s3.cc:6325` and `:6347-6363` at
  v19.2.6; `:6896` and `:6918` at v20.2.4), so neither user can act as the
  other, and `PutOperation::prepare` refuses a second active holder
  (`src/rgw/services/svc_user_rados.cc:257-270`; `:236-249`). The worst case
  is that an admin cannot reactivate an id another user took.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** reproduces it on the RADOS driver until N8; refuses it where
  the store can list users. The driver's user listing answers
  ErrNotImplemented, so there only the active-key index is checked, as in
  radosgw; where the store lists users (memstore), a key create, a user
  create or modify naming a key, or a key modify that leaves the key
  active answers 409 KeyExists for an id another user holds, active or
  not, and writes nothing (`refuseHeldKey`,
  `internal/op/adminuser_sub.go`; `docs/exclusions.md`, "The admin key,
  subuser, caps and quota routes differ from radosgw in seven ways").
- **Upstream:** pending: rgw-bug-reproduction classified it on 2026-10-08
  (benign, non-security); no prior art.
- **Found:** review of phase 1 unit N, Task 5, 2026-10-07; derived from
  the source, not reproduced.

## radosgw's ListParts drops its refusal of an empty upload id

- **Kind:** defect, not security-relevant: the answer to a malformed
  request changes, and the request is still authorized first. Unreproduced:
  derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWListMultipart_ObjStore::get_params` sets `op_ret = -ENOTSUP` for an
    empty `uploadId` and does not return; it then reads
    `part-number-marker` and assigns `op_ret` the result of
    `parse_value_and_bound` for `max-parts`, 0 for any well-formed value,
    and returns that (`rgw_rest.cc:1601-1619`; `:1606-1624`).
  - `RGWListMultipart::execute` goes on when `get_params` returns 0, and
    `get_info` reads the meta object `<key>..meta`, which no upload has, so
    the request answers 404 NoSuchUpload (`rgw_op.cc:6669-6694`;
    `:7593-7633`). The -ENOTSUP the code meant, an errno no S3 error row
    names, would have been 500 UnknownError, as CompleteMultipartUpload's
    matching check answers (`rgw_rest.cc:1584-1587`; `:1589-1592`).
  - `read_obj_policy` skips the meta object for an empty upload id and
    authorizes the request against the object's own ACL (`rgw_op.cc:411`;
    `:441`), so the request is authorized either way.
- **Impact:** a ListParts with `uploadId=` answers 404 NoSuchUpload, or the
  missing-object rule's answer when the key has no object, instead of 500.
  No principal gains anything.
- **Releases:** v19.2.6 and v20.2.4.
- **Fix:** return after setting -ENOTSUP, as
  `RGWCompleteMultipart_ObjStore::get_params` does.
- **rgw-go:** reproduces it: op.ListParts authorizes an empty upload id
  against the object's own ACL and then answers NoSuchUpload
  (`internal/op/listparts.go`), and the s3 handler reads `max-parts`
  without regard to the upload id (`listParts`, `internal/s3/multipart.go`).
- **Upstream:** pending: rgw-bug-reproduction will classify it.
- **Found:** review of phase 1 unit P, Task 9, 2026-10-08, transcribing
  `RGWListMultipart_ObjStore::get_params`; derived from the source, not
  reproduced.

## radosgw's CompleteMultipartUpload Location has no scheme under a configured domain

- **Kind:** defect, not security-relevant: a malformed URI in a response
  document. Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `RGWCompleteMultipart_ObjStore_S3::send_response` writes Location as
    `compute_domain_uri(s)` followed by `/<bucket>/<key>`, or
    `/<tenant>:<bucket>/<key>` (`rgw_rest_s3.cc:4086-4101`; `:4616-4631`).
  - `compute_domain_uri` returns `s->info.domain` as it is when it is not
    empty, and adds `http://` or `https://` only in its fallback, which
    takes `SERVER_NAME`, which beast never sets, or `HTTP_HOST`, or the
    literal `<HTTP_HOST>` when the request sent no Host
    (`rgw_rest.h:805-818`; `:817-830`).
  - `RGWREST::preprocess` sets `s->info.domain` to the configured hostname
    the Host matched, as cut from the Host, and otherwise to `rgw_dns_name`
    as configured (`rgw_rest.cc:2163-2165` and `:2178-2180`; `:2180-2182`
    and `:2200-2202`). `rgw_dns_name` may list several names, which
    `rgw_rest_init` splits at commas and spaces for matching
    (`rgw_rest.cc:215-218`; `:215-218`), so the fallback writes the whole
    list.
- **Impact:** on any gateway with `rgw_dns_name` or a zonegroup hostname,
  every CompleteMultipartUpload's Location is a scheme-less reference such
  as `s3.example.com/bucket/key`, or `a.example.com, b.example.com/bucket/key`
  for a list, and a request without a Host gets `http://<HTTP_HOST>/...`.
  S3's Location is an absolute URI; a client that follows it fails. No
  principal gains anything.
- **Releases:** v19.2.6 and v20.2.4.
- **Fix:** prefix the scheme in `compute_domain_uri`'s domain branch, and
  use the matched name rather than the raw `rgw_dns_name` list.
- **rgw-go:** reproduces it, so both gateways write the same document
  (`completeLocation`, `internal/s3/multipart.go`).
- **Upstream:** pending: rgw-bug-reproduction will classify it.
- **Found:** review of phase 1 unit P, Task 9, 2026-10-08, transcribing
  `compute_domain_uri`; derived from the source, not reproduced.

## radosgw's retried bucket writes are not authorized again

- **Kind:** defect, unfixed at v19.2.6 and v20.2.4: a retried write runs
  under the authorization its request was given before the write it lost
  to. Classification pending: rgw-bug-reproduction will classify it.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/`; each pair of lines is v19.2.6's,
  then v20.2.4's.
  - `rgw_process_authenticated` runs the op's `verify_permission` once,
    before its `execute` (`rgw_process.cc:225` and `:255`; `:225` and
    `:258`). The check reads the bucket's ACL and policy as
    `rgw_build_bucket_policies` loaded them into `s->bucket_acl` and
    `s->iam_policy` when the request began (`rgw_op.cc:547-550` and `:599`;
    `:577-580` and `:629`), through `verify_bucket_permission`
    (`rgw_common.cc:1461-1476`; `:1498-1513`).
  - Seventeen bucket writes retry inside `execute` through
    `retry_raced_bucket_write` (`rgw_op.h:183-196`; `:197-210`): Put and
    DeleteBucketTagging, Put and DeleteBucketReplication,
    PutBucketVersioning, Put and DeleteBucketWebsite, the Swift bucket
    metadata write, Put and DeleteBucketCors, Put and DeleteBucketPolicy,
    PutObjectLockConfiguration, Put and DeletePublicAccessBlock, and Put
    and DeleteBucketEncryption (`rgw_op.cc:1197`, `:1232`, `:1293`,
    `:1339`, `:2828`, `:2924`, `:2970`, `:4959`, `:6100`, `:6130`, `:8110`,
    `:8204`, `:8276`, `:8656`, `:8736`, `:8794`, `:8847`; `:1434`, `:1469`,
    `:1530`, `:1576`, `:3060`, `:3156`, `:3202`, `:5301`, `:6770`, `:6800`,
    `:9036`, `:9151`, `:9224`, `:9637`, `:9717`, `:9775`, `:9828`). The S3
    bucket handler picks the PUT ones by subresource
    (`RGWHandler_REST_Bucket_S3::op_put`, `rgw_rest_s3.cc:4695-4740`;
    `:5240-5286`).
  - Between tries the template calls only `try_refresh_info`, which reads
    the bucket's info and attrs into `s->bucket`
    (`driver/rados/rgw_sal_rados.cc:780-783`; `:798-801`), then the write
    again. Neither it nor the writes run `verify_permission` or load
    `s->bucket_acl` and `s->iam_policy` again: PutBucketTagging's write
    sets the tags over the refreshed attrs and stores them
    (`rgw_op.cc:1197-1201`; `:1434-1438`), and PutBucketPolicy's sets the
    policy (`:8110-8115`; `:9036-9046`).
  - A try fails with -ECANCELED when another write of the bucket instance
    landed after the request loaded it, so the write a retry follows can
    be the one that revoked the requester's permission: an ACL that drops
    their grant, or a policy that denies them the action.
  - PutBucketAcl and PutObjectAcl, which `RGWPutACLs` serves for the bucket
    and the object handlers alike (`rgw_rest_s3.cc:4711` and `:4836`;
    `:5257` and `:5393`), do not retry: on -ECANCELED they answer success
    with nothing written (`rgw_op.cc:5916-5927`; `:6578-6589`; "radosgw's
    PutBucketAcl answers success when its write loses a race", above). No
    object write goes through `retry_raced_bucket_write`, so the gap is
    the bucket writes'.
- **Impact:** a request authorized when it began can, after losing a race
  to the write that revokes its permission, store its change anyway and
  answer success. A requester whom a new bucket policy denies
  s3:PutBucketPolicy can so replace or remove that very policy. The window
  is one request's, from its bucket load to its first write, and opens
  only when the revoking write lands inside it.
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. Before each retry it runs the op's
  permission check again, for every retried bucket write it serves
  (PutBucketAcl, Put and DeleteBucketPolicy, and Put and
  DeleteBucketTagging) and for PutObjectAcl, which it retries where
  radosgw does not: against the bucket that retry read and, for
  PutObjectAcl, the object's head read again. A refusal ends the write
  with 403 AccessDenied and nothing written
  (`RetryRacedWriteReauthorized`, `internal/op/objectattrs.go`;
  `retryBucketWrite`, `internal/op/bucketsubres.go`; `docs/exclusions.md`,
  "A PutBucketAcl that loses a race is retried, and every retried ACL,
  policy or tagging write is authorized again"), since jhoblitt/rgw-go#184
  and #185. The identity's own policies are not read again: each check
  evaluates those of the user record read when the request was
  authenticated (`identityPolicies`, `internal/authz/load.go`), so a retry
  does not see a change to them.
- **Upstream:** pending: rgw-bug-reproduction will classify it.
- **Found:** review of phase 1 unit X, Task 5, 2026-10-07, re-authorizing
  rgw-go's retried bucket writes; derived from the source, not reproduced.

## radosgw's bucket link overwrites the entry point of the bucket it renames onto

- **Kind:** defect, security-relevant, found by us; unfixed at v19.2.6 and
  v20.2.4: a name takeover and an orphaned bucket, not a data hijack.
  Unreproduced: derived from the source.
- **Evidence:** paths are under `src/rgw/driver/rados/`; each pair of
  lines is v19.2.6's, then v20.2.4's.
  - `RGWBucketAdminOp::link` builds the bucket's new key from the uid's
    tenant and `new-bucket-name` (`rgw_bucket.cc:1081-1091`;
    `:1232-1242`) and never reads the entry point under that key: no guard
    asks whether another bucket owns the name.
  - It writes the instance under the new key exclusively (`:1136-1144`;
    `:1287-1295`), which succeeds, as that key carries the bucket's own
    id.
  - `link_bucket` with `update_entrypoint` adds the owner's list entry
    (`:3374`; `:3473`) and stores the entry point with `exclusive` false
    under the `ep_data` tracker (`:3349-3350` and `:3391-3392`;
    `:3449-3450` and `:3490-3491`), which is at version 0, so the write is
    unchecked.
  - So a link whose new key is a name another bucket holds points that
    name at the linked bucket. A plain link without `new-bucket-name` does
    the same when the uid's tenant differs from the bucket's and already
    has a bucket of that name.
- **Impact:** admin-only, as the route needs `buckets=write`, and
  deterministic; no request without that cap reaches it. The name now
  loads the linked bucket, whose own instance and index serve it, so the
  linked bucket's owner does not reach the victim's data. The victim's
  bucket, its instance, index and object heads, is orphaned: its owner's
  requests by name reach the linked bucket and are refused by its ACL, and
  the bucket comes back only through `radosgw-admin bucket link
  --bucket-id`. Triage estimate: CVSS 6.5 within a tenant, 8.7 across
  tenants (S:C).
- **Releases:** v19.2.6 and v20.2.4.
- **rgw-go:** does not reproduce it. A link whose bucket ends under a name
  another bucket's entry point holds answers 409 BucketAlreadyExists
  before writing anything, rename, cross-tenant link and link by
  `bucket-id` alike, and the entry point is written under the version
  read or created exclusively, so a bucket created under the name after
  that check keeps it (`claimName` and `ChangeBucketOwner`,
  `internal/driver/bucketadmin.go`; `docs/exclusions.md`, "Bucket link and
  unlink write differently from radosgw").
- **Upstream:** pending: rgw-bug-reproduction classified it on 2026-10-08;
  not yet disclosed.
- **Found:** phase 1 unit N, Task 6, 2026-10-08, reading the admin bucket
  routes; derived from the source, not reproduced.
