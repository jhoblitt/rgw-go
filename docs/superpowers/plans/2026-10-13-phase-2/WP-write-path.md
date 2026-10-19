# Phase 2 unit WP: write path

Threat model: two kinds of input. A client adversary meets the request parsers this unit adds
and one authorization check, and these get adversarial review (PP-14):
- the checksum headers, and the aws-chunked trailer section, which authentication reads for
  the signed-trailer payload (WP-3, WP-6);
- `?layout`'s `admin=read` cap check (WP-21).

The otp section's input comes from an admin who holds `metadata=write`, phase 1's check. Its
validation still keeps a careless document from faulting the OSD (defect 11). Everything else
defends shared RADOS state against honest mistakes: a checksum record, a compressed block or
an otp object that the other gateway would misread. Nothing here defends against a malicious
RADOS writer. The record decoder refuses what it cannot read (defect 9), which keeps rgw-go
correct, not safe from one.

Source: `research/research-writepath.md` (the report), researched at v19.2.6 and v20.2.4. This
file's citations are retargeted to v19.2.7 (unit F). Section numbers (§2.1 and so on) are the report's. rgw-go
paths are at origin/main 0239625, the report's base. Where this file departs from the report,
the drafting notes says why.

## Scope

Spec §9, phase 2, in its words: "checksums at Tentacle level, compression and placement on the
write path, … the `?layout` extension. Admin adds the otp metadata section." Spec §8's release
rule applies to checksums: "a Tentacle gateway-side feature is available on a Squid cluster when
its persistent form is a separate attribute, as checksums are, and unavailable when it is a
field of a struct the cluster's radosgw would re-encode without it".

| Item | Surface | Tasks | Stored form |
|---|---|---|---|
| Checksums at Tentacle level | PutObject, UploadPart and UploadPartCopy with `x-amz-checksum-algorithm`, `x-amz-sdk-checksum-algorithm`, `x-amz-checksum-<alg>` as a header or an aws-chunked trailer; CreateMultipartUpload's algorithm and `x-amz-checksum-type`; CompleteMultipartUpload's combined checksum; GET and HEAD with `x-amz-checksum-mode`; GetObjectAttributes' `Checksum` and parts; ListParts; the seam POST object (B) writes through | WP-1 to WP-3, WP-5 to WP-9, WP-12 to WP-15 | head attr `user.rgw.cksum` (`rgw::cksum::Cksum` v2); Tentacle's `multipart_upload_info` v4 type and flags and `RGWUploadPartInfo` v6 `cksum` |
| Compression on the write path | the placement's per-class `compression_type` on PutObject, UploadPart, UploadPartCopy, POST object and a streamed CopyObject; the INCOMPRESSIBLE hint | WP-4, WP-10, WP-11, WP-15 | head attr `user.rgw.compression` (`RGWCompressionInfo` v2); the part record's `cs_info`; Ceph's per-codec block framing |
| Placement on the write path | phase 1 serves the rule, the class, the pools and the label. Phase 2 adds the class's compression type and the raw-copy seam lifecycle's transition consumes | WP-10, WP-11 | none new |
| `?layout` | `GET /{bucket}/{key}?layout`, RGWGetObjLayout (D25) | WP-20, WP-21, WP-24 | none: it reads what is stored |
| Admin otp | `GET /admin/metadata/otp` (list); `GET`, `PUT` and `DELETE /admin/metadata/otp?key=user:<uid>` | WP-16 to WP-19, WP-24 | `user:<uid>` in the zone's `otp_pool`, through cls_otp |

Not in this unit: the s3-tests selection port and include list (T: WP-22, WP-23); POST object's
form and policy (B), which writes through WP-5's and WP-6's seams; versioning's MFA check (V13a to V13d),
whose V13a and V13b consume WP-16 and WP-17; lifecycle's transition (C11), which consumes the placement
lookup and WP-11's raw copy; Tentacle's `BucketIndex` (D24).

Exclusions this unit retires or rewrites, at 0239625:
- "Client checksums are ignored until phase 2" (`docs/exclusions.md:2152`), retired by WP-6
  and WP-14;
- "Stored checksums are not shown" (`docs/exclusions.md:4013`), retired by WP-12;
- the checksum line of "Multipart uploads in flight are shared" (`docs/exclusions.md:686-687`),
  rewritten by WP-9;
- the checksum sub-bullet of "aws-chunked framing is read strictly" (`docs/exclusions.md:1798`),
  rewritten by WP-3;
- "A copy that streams its data copies the stored bytes" (`docs/exclusions.md:2207`), rewritten
  by WP-11 (D23);
- "The admin metadata API serves three sections" (`docs/exclusions.md:1646`), rewritten by
  WP-19;
- `?layout`'s 501 in "Tentacle's x-amz-delete-marker is false, or absent, on a missing object"
  (`docs/exclusions.md:4127-4132`), removed by WP-21.

"A requested bucket index answers 501 on Tentacle" (`docs/exclusions.md:2264`) stays (D24).

## Decisions

The index's rulings this unit carries are D21 to D25, each with the report's recommendation as
its ruling (its §8 open questions 1 to 5). The unit's own are WD-1 to WD-17. Every difference
from radosgw below is an exclusion, written by the task named in the last column.

| Id | Decision | Reason | Tasks |
|---|---|---|---|
| D21 | Writes accept CRC32, CRC32C, CRC64NVME, SHA1, SHA256 and SHA512 under AWS's names. They refuse MD5, XXHASH64 and XXHASH128 (no Tentacle type id; main's 9 to 11), BLAKE3 and XXHASH3 with 400 InvalidArgument, before the body. XXHASH3 carries an exclusion: phase 2 takes no XXH3 dependency. Any stored type 0 to 8 is read and rendered, since rendering computes nothing. An UploadPart or a completion of an upload whose stored type rgw-go cannot compute answers 501. | The AWS set where the stored form stays one a v20.2.4 radosgw reads: it indexes its nine-entry table with the stored type ([rgw_cksum.h:88-99](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L88-L99), [rgw_cksum.h:147-149](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L147-L149)). Review Focus 4. | WP-1, WP-6, WP-8 |
| D22 | Squid serves single-object checksums (PutObject, POST object) and verifies and stores a part's own supplied value on its part head. CreateMultipartUpload's algorithm is ignored on Squid, as Squid's radosgw ignores it, so no upload type, part-record checksum or combined checksum is stored. | The upload's type and the part record's checksum are struct fields Squid's radosgw re-encodes without ([rgw_common.h:1508-1538](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L1508-L1538)); `user.rgw.cksum` is a separate attr (spec §8). | WP-6, WP-8, WP-9, WP-12 |
| D23 | A streamed copy of an uncompressed source into a compressing placement is compressed, on both releases. A compressed source's copy stays raw (owner decision 4). | Tentacle's `RGWCopyObjDPF` compresses it ([rgw_op.cc:5825-5884](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5825-L5884)); either gateway reads both forms. The exclusion text of the earlier copy decision is rewritten. | WP-11 |
| D24 | Tentacle's CreateBucketConfiguration `BucketIndex` stays 501, with its exclusion. | It sits in no phase (spec §9). | none |
| D25 | `?layout` is the object op RGWGetObjLayout, for a user with the `admin=read` cap. | It exists only as the object op at both tags (§2.4). | WP-20, WP-21 |
| WD-1 | Every combined CRC is stored `V2\|COMBINED\|FULL_OBJECT`. A CRC upload created COMPOSITE gets the composite: the CRC of the concatenated big-endian part CRCs, flags `V2\|COMBINED\|COMPOSITE`. | Defects 2 and 3: radosgw stores a single-part CRC as a composite and ignores a COMPOSITE request. Both records are ones a Tentacle radosgw decodes and renders consistently. | WP-2, WP-9 |
| WD-2 | A checksum value without an algorithm header names its algorithm: the lone `x-amz-checksum-<alg>` header, or the checksum `x-amz-trailer` names. It is verified. Two value headers, or a value or trailer naming another algorithm than the algorithm header, is 400 InvalidRequest. | Defect 6: radosgw stores such a value unverified; AWS verifies it. | WP-6 |
| WD-3 | An UploadPart naming another algorithm than its upload's is 400 InvalidRequest at the part. | radosgw fails it only at completion ([rgw_op.cc:7121-7131](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7121-L7131)); failing early stores no part completion must refuse. | WP-6 |
| WD-4 | Completion combines exactly the parts the request names, in its order. Its supplied value is any `x-amz-checksum-<alg>` header, CRC64NVME included, and must equal the armored value, or a composite's value without `-N`; anything else is 400 BadDigest. | Defects 1, 4 and 7. The `-N` leniency is radosgw's documented minio case. | WP-6, WP-9 |
| WD-5 | A `partNumber` GET or HEAD with checksum mode renders the part's value bare, COMPOSITE only when its flags say so, as main does. | Defect 5; s3-tests' five `test_multipart_use_cksum_helper_*` expect it ([rgw_rest_s3.cc:569-608](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_rest_s3.cc#L569-L608)). | WP-12 |
| WD-6 | A composite checksum on an object without parts is not shown. A streamed copy drops a composite checksum rather than carry it to a destination without parts. | Squid's radosgw COPY carries the attr to a streamed copy of a multipart source (§4); read back as FULL_OBJECT it would be wrong. | WP-11, WP-12 |
| WD-7 | ListParts reports `ChecksumAlgorithm` and `ChecksumType` from the upload's `multipart_upload_info`. | Defect 8: radosgw reads an attr nothing writes, so never reports them. | WP-12, WP-13 |
| WD-8 | rgw-go names a type as AWS and main do where AWS has it: SHA512 as `x-amz-checksum-sha512` and `ChecksumSHA512`, a stored type 3 as XXHASH3. BLAKE3 keeps Tentacle's `x-rgw-checksum-blake3`. | D21's "AWS's names"; main's table makes sha512 and xxhash3 AWS types ([rgw_cksum.h:93-107](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_cksum.h#L93-L107)). A v20.2.4 radosgw renders `x-rgw-checksum-sha512`. | WP-1, WP-7, WP-13 |
| WD-9 | Compressed blocks are never longer than `rgw_max_chunk_size` of plaintext. zlib's `compressor_message` states the window rgw-go used: -15 for raw deflate, 15 for the zlib wrapper, 31 for gzip, never a narrower one. | A larger block exposes a coexisting radosgw to its ranged-GET loop ("A ranged GET of a compressed object does not spin"); Go's deflate always uses a 32 KiB window, which a narrower one cannot inflate (§4). | WP-4, WP-10 |
| WD-10 | `random` and a codec rgw-go cannot load store the object uncompressed and log once. | radosgw's missing plugin does the same ([rgw_op.cc:4563-4587](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4563-L4587)); `random` is a teuthology setting. | WP-10 |
| WD-11 | Lifecycle's transition copies raw: `copyData` takes a raw flag that skips every compression and checksum filter and keeps the stored attrs. | `transition_obj` streams with no data-processor factory on both tags ([rgw_rados.cc:5417-5487](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5417-L5487)). | WP-11 |
| WD-12 | `?layout` answers `dump_obj_layout`'s error as an error, not a truncated 200. | Defect 10. | WP-21 |
| WD-13 | An otp put is refused with 400 InvalidArgument before any write for a `step_size` of 0, a type other than TOTP, a seed type other than hex or base32, or a NUL in the key. | Defect 11 takes the class's divide out of reach; the type and seed rules are `radosgw-admin mfa create`'s ([rgw_admin.cc:10810-10817](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L10810-L10817)). | WP-19 |
| WD-14 | The otp section applies `update-type` as rgw-go's other sections do. Its put answers Squid's 204 with `RGWX_UPDATE_STATUS: applied` and Tentacle's 200 without it, and `RGWX_UPDATE_VERSION: ver:0,tag:` on both. | radosgw applies every otp put whatever `update-type` says ([rgw_otp.cc:120-140](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_otp.cc#L120-L140), [rgw_otp.cc:70-82](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L70-L82)); the status and version headers are its bytes ([rgw_rest_metadata.cc:234-284](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_metadata.cc#L234-L284)). | WP-18, WP-19 |
| WD-15 | An otp put is refused with 400 InvalidArgument, after every device is checked and before any write, when a device's `seed` is longer than 1024 bytes, counted as `len` of the decoded JSON string. A seed radosgw wrote longer still reads back: only writes are bounded. | Defect 13: the class declares a stack array as long as each seed, which nothing bounds below the OSD's 90 MiB write limit (registry, "cls_otp's otp_set puts an array as long as the seed on the OSD's stack"). The decoded string's bytes are what `std::string::length()` counts, so they size the array. 1024 bytes is 8 times the 128 hex characters of RFC 6238's longest key, SHA-512's 64 bytes, which base32 spells in 104. | WP-19 |
| WD-16 | No code of WP-17, WP-19, V13a or V13b formats a struct holding devices in an unexported field, or a device, or any value holding one, under `%p` or `%w`. | fmt prints those without calling `OTPInfo`'s `Format`, which keeps the seed and `seed_bin` out of every other path (WP-16's godoc). V13b writes the emulator's check methods over the device state WP-17 keeps. | WP-17, WP-19, V13a, V13b |
| WD-17 | rgw-go's otp put is one atomic op: radosgw's remove with FAILOK, `create(false)`, version, mtime and `otp_set`, then a raw omap write of `header` holding exactly the document's ids, `otp_header{set<id>}` v1, byte-identical to the class's. Replay state survives the put, as radosgw's does. | Every class call in one op reads the committed omap ([PrimaryLogPG.cc:7779-7800](https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7779-L7800), [PrimaryLogPG.cc:7856-7877](https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7856-L7877)), so `otp_set` lists the stored ids beside the document's. Class calls alone give exactly the document's ids only when it just drops devices: `otp_set`, then `otp_remove` of the dropped ids. When it adds one device and drops another they fail, as `otp_remove` too reads the committed header, which lacks the added id, and its header write comes last. The raw write covers every case. A put split after its remove would leave an empty object if the gateway died between the two ops. radosgw's put keeps a dropped device's id, after which every `get_all` fails (defect 14). | WP-19 |

## Seams

**Consumed from other units**

- **S2:** the `?layout` route-set file, `internal/s3/layout.go`, and the `op.Env.Layout` field,
  of the `LayoutStore` interface S2 declares in `internal/op/layout.go` with WP-20's signature. WP-21 binds its one
  route there.
- **V6:** `StatObject` follows the OLH when no instance is named, and after a follow
  `ObjectState.Key` is the resolved version's key (`Key.Instance` the version id). `?layout`
  reads that version's head (D25).
- **V11, V14a:** the hot-file order on the S3 write and read handlers (Hot files). WP-7 lands
  after V11 and V14a, and WP-13 after WP-7.
- **Phase 1, unchanged:**
  - `op.ZoneInfo.Placement(rule meta.PlacementRule) (Placement, error)` and
    `op.Placement.Compression` (`internal/op/zone.go:18-19`, `internal/op/zone.go:39`);
  - `meta.CompressionInfo` and its encoder (`internal/meta/compression.go:38`);
  - `meta.UploadPartInfo.Cksum` as raw bytes (`internal/meta/multipart.go:114-117`), and
    `meta.MultipartUploadInfo.CksumType` and `CksumFlags` at v4 on Tentacle
    (`internal/meta/multipart.go:201-260`);
  - `compression.NewDecoder` (`internal/compression/codec.go:51-80`);
  - `meta.Manifest.MarshalJSON` (`internal/meta/manifest.go:53-100`); `op.CheckCaps`;
  - `meta.ZoneParams.OTPPool` (`internal/meta/zone_params.go:32`).

**Produced for other units**

| Seam | Task | Consumer |
|---|---|---|
| `cksum.ParseName(name string) (cksum.Type, bool)`, case-insensitive over Tentacle's nine names and main's `xxhash3`; `cksum.ForeignName(name string) bool` for main's MD5, XXHASH64 and XXHASH128 | WP-1 | BC-30 (which form fields are checksum fields), WP-6 |
| `op.ChecksumRequest{Type cksum.Type; Flags uint16; Expected func() (string, bool)}`, `PutParams.Checksum *ChecksumRequest` | WP-5 | BC-32 (POST object's write) |
| the checksum request parser over a field lookup, so POST object passes its form fields where a request passes headers: `op.ChecksumInput{Field func(name string) (string, bool); Trailers Trailers}` | WP-6 | BC-32 |
| `internal/cls/otp`: `OTPInfo` (`otp_info_t` v1) with its JSON, `OTPCheck` (`otp_check_t`), the check-result constants, `OTPObject(uid meta.UserID) string` (`"user:" + uid.String()`, in `ZoneParams.OTPPool`), and the admin calls `Set`, `GetAll`, `Remove`, `GetCurrentTime` | WP-16 | V13a, which adds `ops_check.go` (`Check`, `LookupCheck`) |
| fakerados `OTPClassAt(now func() time.Time) ClassFunc` and `OTPClass()`, assembled from `otpAdminMethods(now)` and `otpCheckMethods(now)`, an empty table in `cls_otp_check.go`, on the clock `now`, which stands for the OSD's; each device's `last_checks` and `last_success` kept in the instance type | WP-17 | V13b, which fills `cls_otp_check.go` |
| the placement lookup for a transition's target class: phase 1's `op.ZoneInfo.Placement(rule)`, which is `valid_placement` plus the pools and compression type. WP adds no new placement seam | existing | C11 |
| `copyData`'s raw flag: the stored bytes and attrs, no compression or checksum filter (WD-11) | WP-11 | C11 |
| `internal/op/multipart.go` and `internal/op/object.go`'s checksum fields, landed first | WP-5 | L5a and V's fields, line-local after it |

The parser seam sketch, for BC-32's use (names are the implementer's; the shape binds):

```go
type Trailers interface{ Trailer(name string) (string, bool) }
type UploadChecksum struct{ Type cksum.Type; Flags uint16 }
func ChecksumForPut(in ChecksumInput, rel denc.Release) (*ChecksumRequest, error)
func ChecksumForPart(in ChecksumInput, up UploadChecksum, rel denc.Release) (*ChecksumRequest, error)
func ChecksumForCreate(in ChecksumInput, rel denc.Release) (UploadChecksum, error)
func SuppliedForComplete(in ChecksumInput) (*ChecksumRequest, error)
```

## Hot files

| File | Tasks here | Other units | Rule |
|---|---|---|---|
| `internal/op/{object,multipart,objectread}.go`, `internal/memstore/{object,multipart}.go`, the fakes | WP-5 | V6, V8a, L5a | WP-5 adds every field this unit needs, in one PR, early. Other units' fields are line-local. The task that changes an interface regenerates the fakes and updates memstore. |
| `internal/driver/put.go`, `stripe.go` (`putStream`, the tail writer) | WP-10 (a), WP-10 (b), WP-8 | V7a works in `headwrite.go` | in that order: WP-10 (a) splits the body into a plaintext stage and a stored stage; WP-10 (b) and then WP-8 each add one stage |
| `internal/driver/headwrite.go` | WP-10 (a) (the head's hint) | V7a, L5a | one line-local hint argument; merged apart from V7a, never in parallel |
| `internal/driver/mp_part.go`, `mp_upload.go`, `mp_complete.go` | WP-10 (b), WP-8, WP-9 | V7b (versioned completion), L5a (the upload's lock fields) | WP-10 (b), then WP-8, then WP-9 on `mp_part.go`. `CreateUpload`'s info literal is line-local. The combination lives in a new `mp_cksum.go`, called once from completion's assembly loop. |
| `internal/driver/copy.go` | WP-11 | V7b | merged one after the other |
| `internal/op/{putobject,uploadpart,initmultipart,completemultipart}.go` | WP-6 (one call each; the parsing in `internal/op/checksumreq.go`) | V11, BC-26 | the controller merges WP-6, V11 and BC-26 one after the other, never in parallel on one op |
| `internal/op/getobject.go`, `objectread.go`, `listparts.go` | WP-12 | V6, V14a, BC-14 | line-local; never in parallel |
| `internal/s3/putobject.go`, `multipart.go`, `getobject.go`, `getobjattrs.go` | WP-7, WP-13 | V11, V14a, L5b, C8, BC-26 | Each task adds its headers in its own helper (WP's in `internal/s3/checksum.go`), one call line per site; whichever lands first, the later rebases (index Order, hot-file rule). |
| `internal/driver/metadata.go`, `internal/op/adminmetadata.go` | WP-18, WP-19 | phase 3's roles, user_policy, group and topic sections | WP-18 turns the switches into tables, one file per section; WP-19 and phase 3 each add a file and a row |
| `internal/cls/otp/` | WP-16, WP-19 | V13a | WP-16 owns the package and its types; WP-19 adds `otp_header`'s encoding to `types.go`; V13a adds `ops_check.go` |
| `internal/testutil/fakerados/cls_otp*.go` | WP-17 | V13b | WP-17 owns `cls_otp.go`; V13b writes only `cls_otp_check.go` |
| `internal/auth/chunked.go`, `payload.go`, `internal/op/identity.go` | WP-3 | BC-31 adds a new file and edits none of these | single owner |
| the T unit's s3-tests lists, the ledgers and known-differences files | WP-14 | every unit's recording | after WP-22, WP-14 edits only its own lines; recordings land one at a time through the controller |
| `docs/exclusions.md`, `docs/ceph-upstream-bugs.md`, `docs/cgo-limitations.md` | most tasks | every unit | append-only per PP-5; each PR edits only its own entries |

## Tasks

Squid has no checksum code: v19.2.6 has no `rgw_cksum*` or `rgw_crc_digest.h` file and no
GetObjectAttributes, and its only checksum references are the aws-chunked trailer framing
(§2). Every checksum citation below is therefore v20.2.4's alone; the Squid behaviour is D22's.
The compression sources are the same at both tags but for the QAT and UADK offload paths
(`git diff v19.2.6 v20.2.4 --stat -- src/compressor src/rgw/rgw_compression.cc`), so a
v20.2.4-only citation there stands for both. cls_otp is identical at both tags but for the
client's `otp_check`.

### Task WP-1: checksum types and record

- **Layer:** pure library. **Files:** create `internal/cksum/{doc.go,type.go,record.go}` and
  their suite; a leaf package importing `denc` only. **Cluster:** no.
- **Consumes:** `denc`. **Produces:**
  - `type Type uint16` with Tentacle's nine ids, none 0 to crc64nvme 8;
  - `ParseName(name string) (Type, bool)` and `ForeignName(name string) bool` (Seams);
  - `(Type).Info() Info`, with `Info{Name, Header, Element string; DigestSize int; AWS, CRC bool}`;
  - `(Type).Writable() bool`, D21's six;
  - `type Cksum struct{ Type Type; Digest []byte; Flags uint16 }`, with
    `(Cksum).Encode(e *denc.Encoder, r denc.Release)`, `DecodeCksum(d *denc.Decoder) Cksum` and
    `(Cksum).Armor() string`;
  - `(Cksum).Composite() bool`, `(Cksum).ObjectType(multipart bool) string` and
    `(Cksum).PartType() string`, each answering `FULL_OBJECT` or `COMPOSITE`;
  - `FlagsOf(Type) uint16`, `Permitted(Type, flags uint16) bool`, and `FlagV2`,
    `FlagComposite`, `FlagFullObject`, `FlagCombined`.
- **Behaviour:**
  1. **Types.** none 0, crc32 1, crc32c 2, xxh3 3, sha1 4, sha256 5, sha512 6, blake3 7,
     crc64nvme 8, with digest sizes 4, 4, 8, 20, 32, 64, 32, 8 ([rgw_cksum.h:41-52](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L41-L52),
     [rgw_cksum.h:88-99](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L88-L99)). crc32, crc32c and crc64nvme are CRCs.
  2. **Names.** `ParseName` matches case-insensitively, as `parse_cksum_type` does
     ([rgw_cksum.h:293-300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L293-L300)), over Tentacle's names and main's `xxhash3` for
     type 3 ([rgw_cksum.h:41-56](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_cksum.h#L41-L56)).
     `ForeignName` is true for main's `md5`, `xxhash64` and `xxhash128`, which have no Tentacle
     id.
  3. **Header and element names** follow WD-8:
     - an AWS type's header is `x-amz-checksum-<name>`, any other's `x-rgw-checksum-<name>`, and
       the element `Checksum<NAME>` ([rgw_cksum.h:177-192](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L177-L192));
     - rgw-go's AWS set is v20.2.4's (crc32, crc32c, sha1, sha256, crc64nvme) plus main's sha512
       and xxhash3 ([rgw_cksum.h:93-107](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_cksum.h#L93-L107));
     - blake3 stays `x-rgw-checksum-blake3`.
  4. **The armor** is the base64 of the raw digest ([rgw_cksum.h:199-207](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L199-L207)). A CRC
     digest is big-endian ([rgw_crc_digest.h:41-121](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crc_digest.h#L41-L121)).
  5. **The record** is `Cksum::encode`: ENCODE_START(2, 1), u16 type, u16 digest size, the
     digest, u16 flags. A v1 record decodes with flags 0
     ([rgw_cksum.h:258-281](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L258-L281)).
  6. **Refusals (defect 9).** A decode fails for a type above 8 and for a digest size other than
     the type's, so a reader turns either into "no checksum". `Encode` is reached only with a
     `Cksum` that `Finish` (WP-2) or `DecodeCksum` built, neither of which yields one; a
     `Valid() error` method lets the driver (WP-8) check before it sets the attr.
  7. **Flags.** V2 0x1, COMPOSITE 0x2, FULL_OBJECT 0x4, COMBINED 0x8. `Composite` means both
     COMPOSITE and COMBINED ([rgw_cksum.h:104-113](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L104-L113),
     [rgw_cksum.h:163-175](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L163-L175)).
  8. **`ObjectType`** is `get_checksum_type`: FULL_OBJECT for a non-multipart object; for a
     multipart one FULL_OBJECT only when V2 is set and the record is not composite, else
     COMPOSITE ([rgw_cksum.h:382-399](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L382-L399)). **`PartType`** is main's
     `get_part_checksum_type`: COMPOSITE only when the flags say so (WD-5).
  9. **`FlagsOf`** is `cksum_flags_of`: FULL_OBJECT for a CRC, 0 for none, COMPOSITE otherwise.
     **`Permitted`** is `permitted_cksum_algo_and_type`: none passes; COMPOSITE passes unless
     crc64nvme; FULL_OBJECT or 0 passes only for a CRC ([rgw_cksum.h:340-356](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L340-L356)).
- **Specs:**
  - the type, name, size and flag tables as `DescribeTable` rows, `xxhash3` and `XXH3` both
    type 3, `MD5` foreign, `FOO` neither;
  - v1 and v2 records from hand-built encodings of `Cksum::encode`; WP-14 adds goldens captured
    from radosgw-written attrs to this suite (ceph-dencoder has no `rgw::cksum::Cksum`);
  - each refusal: type 9, type 65535, a digest size of 64 on a crc32, a truncated record;
  - `ObjectType` over every flag set radosgw writes: 0, an upload's flags without V2, and the
    two combined forms, multipart and not.
- **Exclusions and registry:** the registry entry for defect 9 (security, privileged: a RADOS
  writer of the attr or a newer radosgw; unreproduced), also sent to the rgw-bug-reproduction
  session. No exclusion: rgw-go writes nothing radosgw would not.
- **Depends on:** nothing. **PR:** one. **Brief:** about 150 lines.

### Task WP-2: digests and combiners

- **Layer:** pure library. **Files:** create `internal/cksum/{digest.go,combine.go}` and specs.
  **Cluster:** no.
- **Consumes:** WP-1. **Produces:**
  - `NewHash(t Type) (hash.Hash, error)`, whose `Sum` is the stored digest, an error for a type
    that is not `Writable`;
  - `Finish(t Type, h hash.Hash, flags uint16) Cksum`;
  - `NewCombiner(t Type, flags uint16) Combiner`, with
    `Combiner interface{ Append(part Cksum, partSize uint64) error; Final() Cksum }`.
- **Behaviour:**
  1. **The CRCs.** CRC32 is IEEE and CRC32C is Castagnoli, from `hash/crc32`. CRC64NVME is
     `hash/crc64` with the reflected NVMe polynomial, init and xorout all ones as `hash/crc64`
     applies them. Go's `Sum` appends big-endian, which is the stored order.
  2. **The digests** are `crypto/sha1`, `crypto/sha256` and `crypto/sha512`.
  3. **The combiner** follows [rgw_cksum.cc:96-155](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.cc#L96-L155):
     - a CRC upload combines `crc(a||b)` from `crc(a)`, `crc(b)` and `len(b)`
       ([rgw_cksum.cc:31-89](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.cc#L31-L89)), and ORs
       FULL_OBJECT|COMBINED into the flags;
     - any other type digests the concatenated raw part digests and ORs COMPOSITE|COMBINED.
  4. **WD-1's two corrections.** A CRC upload whose flags say COMPOSITE gets the composite: the
     same CRC over the concatenated big-endian part CRCs, flags `V2|COMBINED|COMPOSITE`. Every
     combined CRC is stored `V2|COMBINED|FULL_OBJECT`, a single part included.
  5. **The CRC combination** is zlib's GF(2) operator method, generic over 32 and 64 bits: square
     the shift operator for each bit of `len(b)` and apply it to `crc(a)`, then XOR `crc(b)`. The
     brief states it in prose; no lookup table is copied.
  6. `Append` refuses a part of another type.
- **Specs:**
  - radosgw's vectors: CRC32 `mLLFvQ==`, CRC32C `ldwuSw==`
    ([test_rgw_cksum.cc:106-143](https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L106-L143)), and the awscli CRC64NVME values
    `wiBA+PSv41M=` and `oa2U66pdPLk=` ([test_rgw_cksum.cc:440-467](https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L440-L467));
  - the s3-tests values: 1024 × `A` gives SHA256 `arcu6553sHVAiX4MjW0j7I7vD4w6R+Gz9Ok0Q9lTa+0=` and
    CRC64NVME `Qeh8oXvGiSo=`; the 5 MiB A, B and C parts combine to CRC32 `WgDhBQ==`, CRC32C
    `xU+Krw==`, CRC64NVME `i+6LR0y3eFo=`, SHA1 `sizjvY4eud3MrcHdZM3cQ/ol39o=-3` and SHA256
    `uWBwpe1dxI4Vw8Gf0X9ynOdw/SS6VBzfWm9giiv1sf4=-3`;
  - the property crc(a‖b) = combine(crc(a), crc(b), len(b)) over random splits, with lengths 0,
    1 and 2^20+3, for each CRC;
  - a COMPOSITE CRC32 upload, and a single-part CRC32 upload stored FULL_OBJECT with V2.
- **Exclusions and registry:** WD-1's exclusion, naming defects 2 and 3; their registry
  entries (S3-correctness, unreproduced; WP-14 reproduces them).
- **Depends on:** WP-1. **PR:** one. **Brief:** about 180 lines.

### Task WP-3: trailer values

- **Layer:** authentication. **Files:** modify `internal/auth/chunked.go`, `payload.go`; add
  the `Trailers` field to `op.AuthResult` (`internal/op/identity.go:58-81`). **Cluster:** no.
- **Consumes:** phase 1's aws-chunked reader. **Produces:** `op.AuthResult.Trailers op.Trailers`
  (Seams): nil for a payload with no trailer, and answering only after `Body` has returned its
  EOF.
- **Behaviour:**
  1. radosgw puts each trailer `x-amz-trailer` names into the request's environment as it
     completes the body ([rgw_auth_s3.cc:1459-1488](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1459-L1488)).
  2. rgw-go parses the trailer section as lines, under phase 1's bound and rules ("aws-chunked
     trailer sections are read line by line", `docs/exclusions.md:1861`), for the signed-trailer
     and the unsigned-trailer payloads alike. Today it parses only the signed one
     (`internal/auth/chunked.go:382-433`, the early return at `internal/auth/chunked.go:395-398`).
  3. A trailer's value is that of the first line bearing exactly a listed name, untrimmed, as
     phase 1 reads the signed section. Unlisted lines stay ignored.
  4. `Trailer` before EOF answers not found. The signed payload's signature check is unchanged,
     and still runs before the trailers are offered.
- **Specs:** both payloads with a CRC32 trailer; a listed name that never arrives; a value asked
  for before EOF; the existing 1 KiB section bound on the unsigned payload; the signature
  mismatch still SignatureDoesNotMatch with no trailer offered.
- **Exclusions and registry:** rewrites the checksum sub-bullet of "aws-chunked framing is read
  strictly" (`docs/exclusions.md:1798`): values are now kept.
- **Review:** adversarial (an authentication parser).
- **Depends on:** nothing. **PR:** one. **Brief:** about 120 lines.

### Task WP-4: block encoders

- **Layer:** pure library. **Files:** create `internal/compression/encode.go` and specs.
  **Cluster:** no.
- **Consumes:** klauspost/compress (zstd, snappy) and pierrec/lz4/v4, already in go.mod, and
  `compress/flate`; no new dependency. **Produces:**
  - `type EncoderOptions struct{ ZlibLevel, ZlibWinsize, ZstdLevel int }`;
  - `NewEncoder(codec string, o EncoderOptions) (Encoder, error)`, `ErrUnknownCodec` otherwise;
  - `Encoder interface{ Encode(dst, src []byte) (block []byte, message *int32, err error) }`.
- **Behaviour:** each codec's Ceph framing, so either gateway decodes the block (§2.2):
  1. **zlib:** one byte 0, then a deflate stream at the level
     ([ZlibCompressor.cc:94-154](https://github.com/ceph/ceph/blob/v20.2.4/src/compressor/zlib/ZlibCompressor.cc#L94-L154); the
     options [global.yaml.in:768-781](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/global.yaml.in#L768-L781)). WD-9's message: -15 for a
     negative winsize (raw deflate), 15 for a winsize of 8 to 15 (the zlib wrapper), 31 above 15
     (gzip). Never a narrower window than the 32 KiB Go's deflate uses.
  2. **zstd:** a le32 decompressed length, then one frame at the level klauspost maps from
     `compressor_zstd_level` ([ZstdCompressor.h:29-60](https://github.com/ceph/ceph/blob/v20.2.4/src/compressor/zstd/ZstdCompressor.h#L29-L60)).
  3. **lz4:** a u32 count of 1, then one (u32 origin_len, u32 compressed_len) pair, then the
     block ([LZ4Compressor.cc:36-80](https://github.com/ceph/ceph/blob/v20.2.4/src/compressor/lz4/LZ4Compressor.cc#L36-L80)).
  4. **snappy:** a raw snappy block.
- **Specs:** each codec round-trips through phase 1's `NewDecoder`; the framing bytes (zlib's
  leading 0, zstd's length, lz4's count and pair); the message per winsize, including 8, which
  the registry's "compressor_zlib_winsize 8" entry names, written as 15; an empty input; an
  unknown codec.
- **Exclusions and registry:** none (the zlib message choice is WP-10's exclusion).
- **Depends on:** nothing. **PR:** one. **Brief:** about 140 lines.

### Task WP-5: the checksum contract on the op and store seams

- **Layer:** op contract, with memstore and the fakes. **Files:** modify
  `internal/op/{object,multipart,objectread}.go`; create `internal/op/checksum.go`; modify
  `internal/memstore/{object,multipart}.go`, the regenerated `opfakes`, and
  `internal/op/conformance`. **Cluster:** no.
- **Consumes:** WP-1; WP-2 for memstore's computation. **Produces** (additive fields):
  - `ChecksumRequest` and `UploadChecksum` (Seams), in `checksum.go`; `Expected` is called once
    the body is read, so a trailer's value is there;
  - `PutParams.Checksum *ChecksumRequest` (PutObject and PutPart);
  - `PutResult.Checksum *cksum.Cksum`;
  - `UploadParams.Checksum UploadChecksum` and `Upload.Checksum UploadChecksum`;
  - `Upload.SuppliedChecksum *ChecksumRequest`, request-scoped like `Upload.WriteTag`, so
    `Complete`'s signature does not change;
  - `PartResult.Checksum` and `Part.Checksum`, `*cksum.Cksum`;
  - `ObjectState.Checksum *cksum.Cksum`, nil when absent or undecodable;
  - `ObjectPart.Checksum *cksum.Cksum`.
- **Behaviour:** memstore computes, verifies and stores as the driver will (WP-8, WP-9), so the
  op tasks' specs run against it. Its conformance cases run against both stores once the
  driver tasks land:
  1. a verified checksum is stored and returned on stat;
  2. a mismatch is BadDigest, with nothing written;
  3. a part's type must match its upload's;
  4. completion combines exactly the listed parts (WD-4).
- **Specs:** the four conformance cases on memstore; the fakes regenerate cleanly; every phase 1
  suite passes unchanged.
- **Exclusions and registry:** none.
- **Depends on:** WP-1, WP-2. **PR:** one. **Brief:** about 200 lines.

### Task WP-6: checksum requests

- **Layer:** op. **Files:** create `internal/op/checksumreq.go` and specs; one call each in
  `internal/op/{putobject,uploadpart,initmultipart,completemultipart}.go`. **Cluster:** no.
- **Consumes:** WP-1, WP-3's `Trailers`, WP-5. **Produces:** `ChecksumInput` and the four
  parser functions (Seams), which BC-32 calls with POST object's form fields.
- **Behaviour:**
  1. **The algorithm** of a PutObject or UploadPart, first match wins:
     1. `x-amz-checksum-algorithm`, else `x-amz-sdk-checksum-algorithm`, radosgw's order
        ([rgw_cksum_pipe.h:65-82](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L65-L82));
     2. else the one `x-amz-checksum-<alg>` header present (WD-2);
     3. else the checksum `x-amz-trailer` names (WD-2);
     4. else, for a part, its upload's type and flags ([rgw_op.cc:4471-4472](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4471-L4472));
     5. else none: nothing is computed.
  2. **Names** go through `cksum.ParseName`. Each refusal is 400 InvalidArgument, radosgw's
     EINVAL ([rgw_cksum_pipe.cc:40-73](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.cc#L40-L73),
     [rgw_op.cc:4601-4611](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4601-L4611)):
     - an algorithm header with no value, or naming no algorithm;
     - a `ForeignName` (MD5, XXHASH64, XXHASH128): Review Focus 4;
     - a Tentacle type that is not `Writable` (XXHASH3 or XXH3, BLAKE3), D21.
  3. **Conflicts** are 400 InvalidRequest (WD-2): two value headers; a value header or a trailer
     naming another algorithm than the algorithm header.
  4. **The expected value** is the value header's, or the trailer's through `Trailers` once the
     body is read. With neither, the computed checksum is stored unverified, as radosgw and the
     AWS SDKs' flow expect.
  5. **A part.**
     - A part naming another algorithm than its upload's is 400 InvalidRequest (WD-3).
     - A part, or a completion, of an upload whose stored type rgw-go cannot compute is 501
       NotImplemented with nothing written (D21): xxh3, blake3, or a type above 8 that a newer
       radosgw stored.
  6. **CreateMultipartUpload** ([rgw_rest_s3.cc:4507-4521](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4507-L4521)):
     - the same two algorithm headers. A name that is no algorithm is none, without an error,
       as radosgw takes it. An algorithm D21 refuses is 400 InvalidArgument, as on PutObject
       (the notes say why);
     - `x-amz-checksum-type`: `full_object` gives FULL_OBJECT and `composite` COMPOSITE,
       case-insensitively; any other value gives 0; absent, `FlagsOf`
       ([rgw_cksum_pipe.h:138-154](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L138-L154));
     - a pair `Permitted` refuses is 400 InvalidRequest ([rgw_cksum.h:340-356](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L340-L356)).
  7. **CompleteMultipartUpload.** The supplied value is the `x-amz-checksum-<alg>` header naming
     the upload's type, CRC64NVME included (WD-4; radosgw's scan stops before it,
     [rgw_cksum_pipe.h:120-136](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L120-L136)). A value header naming another type is the
     comparison's 400 BadDigest. It travels in `Upload.SuppliedChecksum`.
  8. **Squid (D22).** CreateMultipartUpload's algorithm and type are ignored, with no error. A
     part takes no upload type. A part's own value follows rules 1 to 4. Completion's supplied
     value is ignored.
  9. **Where in the flow.** Every refusal comes where radosgw's Factory runs: after
     authorization, before the body is read ([rgw_op.cc:4563-4616](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4563-L4616)).
- **Specs** (counterfeiter fakes and memstore):
  - a table from header sets to `ChecksumRequest` or error, per op and release, for each rule;
  - **Review Focus 4: an algorithm a v20.2.4 radosgw cannot read is refused at write.** Rows: MD5,
    XXHASH64 and XXHASH128, in any case, as `x-amz-checksum-algorithm`,
    `x-amz-sdk-checksum-algorithm`, a lone value header and an `x-amz-trailer`, on PutObject,
    UploadPart and CreateMultipartUpload. Each is 400 InvalidArgument and the store is never
    called, so nothing is stored. A part, and a completion, of an upload whose stored type is
    9, 10 or 11 answer 501 with `PutPart` and `Complete` never called;
  - the same shape for XXHASH3 and BLAKE3;
  - the refusals reach no body read: the fake's body reader is untouched;
  - CompleteMultipartUpload with a CRC64NVME value, and with a value of another type.
- **Exclusions and registry:**
  - retires "Client checksums are ignored until phase 2" (`docs/exclusions.md:2152`), with
    WP-14 confirming the recorded outcomes;
  - new: D21's refusals with XXHASH3's no-dependency reason; WD-2's inference and conflicts;
    WD-3; D22's Squid limits; CreateMultipartUpload's refused algorithm;
  - the registry entries for defects 4 (the part half) and 6: S3-correctness, unreproduced;
    WP-14 reproduces them.
- **Review:** adversarial (a client-facing parser).
- **Depends on:** WP-3, WP-5. **PR:** one. **Brief:** about 200 lines.

### Task WP-7: checksum headers on writes

- **Layer:** S3 handler. **Files:** create `internal/s3/checksum.go`; one call each in
  `internal/s3/putobject.go` and `multipart.go`. **Cluster:** no.
- **Consumes:** WP-6; WP-1's names. **Produces:** the write responses' checksum headers and
  elements.
- **Behaviour:**
  1. The handler hands WP-6 a `Field` lookup over the request's headers and the request's
     `AuthResult.Trailers`.
  2. PutObject, UploadPart and UploadPartCopy answer `x-amz-checksum-type: FULL_OBJECT` and the
     value header for an AWS type ([rgw_rest_s3.cc:2891-2925](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2891-L2925)),
     with WD-8's names.
  3. CreateMultipartUpload answers `x-amz-checksum-algorithm` upper-cased and no
     `x-amz-checksum-type` ([rgw_rest_s3.cc:4540-4542](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4540-L4542)); nothing on Squid.
  4. CompleteMultipartUpload answers `Checksum<ALG>`, with `-N` for a composite, and
     `ChecksumType` ([rgw_rest_s3.cc:4635-4640](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4635-L4640)); nothing on Squid.
- **Specs:** each response per release, bodies byte-compared with xmltext; a request with no
  checksum sends neither header; SHA512's `x-amz-checksum-sha512`.
- **Exclusions and registry:** WD-8's naming, which differs from a v20.2.4 radosgw's
  `x-rgw-checksum-sha512`.
- **Depends on:** WP-6; V11 and V14a by the hot-file order. **PR:** one. **Brief:** about 140
  lines.

### Task WP-8: PUT and part checksums

- **Layer:** driver. **Files:** modify `internal/driver/put.go`, `mp_part.go`, `stat.go`.
  **Cluster:** no.
- **Consumes:** WP-2, WP-5, WP-10 (b)'s stages. **Produces:** the stored `user.rgw.cksum` on
  heads and part heads, the part record's checksum, and `ObjectState.Checksum`.
- **Behaviour:**
  1. A plaintext sink, WP-10 (a)'s stage, hashes the body beside the MD5 with `cksum.NewHash`.
  2. After the body, radosgw's order ([rgw_op.cc:4757-4790](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4757-L4790)): the length check,
     aws4 completion, the second quota check, Content-MD5 (400 BadDigest), the supplied ETag
     (422), then the checksum.
     - The checksum compares the expected value with the computed armor as strings
       ([rgw_cksum_pipe.h:193-211](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L193-L211)).
     - A mismatch is 400 BadDigest before the head write, with the tails written so far
       discarded as phase 1 discards them on a Content-MD5 mismatch.
  3. `user.rgw.cksum` goes into the same guarded head write, with radosgw's flags: 0 on an
     object and the upload's flags on a part. `finalize()` leaves V2 clear on every stored
     checksum ([rgw_cksum_pipe.h:187-191](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L187-L191)).
  4. A part head carries the part's `user.rgw.cksum` on both releases. The part record carries it
     as `RGWUploadPartInfo.cksum` at version 6 on Tentacle only
     ([rgw_putobj_processor.cc:525-584](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L525-L584)).
  5. `CopyPart` hashes the decoded source with its upload's type through the same stage.
  6. `StatObject` decodes the attr into `ObjectState.Checksum`; a record WP-1 refuses gives nil.
  7. **The store's own guard.** A `ChecksumRequest` whose type is not `Writable` is refused with
     InvalidArgument before any write, and `Cksum.Valid` is checked before the attr is set, so no
     caller can store a type above 8 (Review Focus 4).
- **Specs** (fakerados):
  - the attr's bytes for each writable type, an object's and a part's;
  - **Review Focus 4: the store never writes a type a v20.2.4 radosgw cannot read.**
    PutObject and PutPart given a request of type 9, 10, 11 or 65535, as a caller that skipped
    the op would pass it, write no tail, head or index entry. Every `user.rgw.cksum` the suite
    writes decodes under WP-1 with a type of at most 8;
  - a mismatch writes no head and leaves no tail; both Content-MD5 and the checksum wrong is
    the Content-MD5 BadDigest;
  - the part record on each release;
  - a stat of a v1 record, a type-9 record and a truncated one: nil, with no error;
  - the emulator needs nothing new: the attr rides the head write's existing xattr step.
- **Exclusions and registry:** none of its own (WP-6 writes the request rules).
- **Depends on:** WP-2, WP-5, WP-10 (b). **PR:** one. **Brief:** about 180 lines.

### Task WP-9: multipart checksums

- **Layer:** driver. **Files:** modify `internal/driver/mp_upload.go`, `mp_list.go`,
  `mp_complete.go`; create `internal/driver/mp_cksum.go`. **Cluster:** no.
- **Consumes:** WP-2, WP-5, WP-8. **Produces:** the upload's stored type and flags, ListParts'
  part checksums, and the combined checksum.
- **Behaviour:**
  1. **CreateUpload** writes the type and flags into `multipart_upload_info` at v4 on Tentacle,
     and at v2 without them on Squid ([rgw_common.h:1552-1592](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1552-L1592),
     [rgw_common.h:1508-1538](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L1508-L1538)), as radosgw's init does
     ([rgw_op.cc:7003-7010](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7003-L7010), [rgw_sal_rados.cc:4150-4154](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4150-L4154)).
  2. **GetUpload** decodes them into `Upload.Checksum`. **ListParts** returns each part's
     checksum from its record.
  3. **Complete**, on Tentacle, for an upload with a type ([rgw_op.cc:7211-7341](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7211-L7341)):
     1. Under the completion lock, it combines exactly the parts the request names, in its order,
        from the part records the assembly already reads, through WP-2's combiner (WD-4,
        defect 1). The combination is one function in `mp_cksum.go`, called once from the
        assembly loop.
     2. A named part without a checksum, or of another type, is 400 InvalidRequest
        ([rgw_op.cc:7105-7131](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7105-L7131)).
     3. The armored value carries `-<parts named>` when the result is composite.
     4. The supplied value (WD-4) is compared before the head write; a mismatch is 400
        BadDigest.
     5. The combined record goes into the head's attrs.
     6. A retried completion answers from the stored checksum ([rgw_op.cc:7474-7501](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7474-L7501)).
  4. **Squid** writes v2 info and combines nothing (D22).
- **Specs** (fakerados):
  - two- and three-part uploads, composite and full-object, each CRC and digest type;
  - an upload holding a part the request omits (defect 1);
  - the supplied value exact, without `-N`, with a trailing byte (BadDigest, defect 7), and as
    CRC64NVME (defect 4);
  - a part record written as radosgw writes it, at the upload's flags without V2;
  - the release split of the info and the part record; a retried completion.
- **Exclusions and registry:**
  - rewrites the checksum line of "Multipart uploads in flight are shared"
    (`docs/exclusions.md:686-687`): either gateway may extend an upload the other began, and
    their types must agree, or completion refuses with 400 InvalidRequest, as radosgw does;
  - WD-4's exclusion;
  - the registry entries for defects 1, 4 (the completion half) and 7: S3-correctness,
    unreproduced; WP-14 reproduces them.
- **Depends on:** WP-2, WP-5, WP-8. **PR:** one. **Brief:** about 200 lines.

### Task WP-10: compression on PUT and parts

- **Layer:** driver. **Files:** modify `internal/driver/put.go`, `stripe.go`, `mp_part.go`,
  `headwrite.go` (the head's hint), `writer.go` (the codec options). **Cluster:** no.
- **Consumes:** WP-4 for PR (b); `op.Placement.Compression`. **Produces:**
  - PR (a): `putStream` split into a plaintext stage, with sinks (the MD5, WP-8's checksum),
    and a stored stage that feeds the tail writer; a hint flag carried to every tail write and
    to the head's `write_full`;
  - PR (b): the compressing stored stage, and `user.rgw.compression` on heads and in part
    records.
- **Behaviour:**
  1. **PR (a) changes no behaviour.** Tails keep `SetAllocHint(0, 0, 0)`
     (`internal/driver/stripe.go:308`, `internal/driver/stripe.go:540`); the head keeps no hint.
  2. **Choosing a compressor.** The destination placement's class `compression_type`, "none"
     when unset ([rgw_zone.cc:356-365](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_zone.cc#L356-L365),
     [rgw_zone.cc:356-365](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_zone.cc#L356-L365)),
     which phase 1 resolves into `op.Placement.Compression`. `random` and a codec rgw-go cannot
     load store the object uncompressed and log once (WD-10; radosgw's `random` is
     [rgw_op.cc:4088-4108](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4088-L4108),
     [rgw_op.cc:4297-4317](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4297-L4317)).
  3. **Encrypted writes.** radosgw skips the compressor for an encrypted write unless the
     zonegroup has `compress-encrypted` ([rgw_op.cc:4341-4371](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4341-L4371),
     [rgw_op.cc:4563-4587](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4563-L4587)). No phase 2 write is encrypted, so the rule has no
     case until phase 4; the stage takes the flag so phase 4 sets it.
  4. **Blocks.** The plaintext is cut into `rgw_max_chunk_size` blocks, the last shorter, each
     one encoder call (WD-9; [rgw_compression.cc:44-88](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_compression.cc#L44-L88),
     [rgw_rest.cc:1073-1106](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1073-L1106)):
     - each block records `{old_ofs = plaintext offset, new_ofs = end of the previous block, len}`;
     - a first block that fails stores the whole object raw; a later failure is 500, radosgw's
       EIO.
  5. **What the tail writer sees** is the compressed stream: the head chunk and the stripes are
     cut from it, and the manifest's size is the compressed size.
  6. **What stays on the plaintext:** the MD5, the declared length, `rgw_max_put_size`, both
     quota checks and WP-8's checksum.
  7. **The attr.** `user.rgw.compression` is `RGWCompressionInfo` v2 {type, orig_size = the
     plaintext size, compressor_message, blocks} ([rgw_op.cc:4454-4463](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4454-L4463),
     [rgw_op.cc:4686-4695](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4686-L4695), [rgw_compression_types.h:18-73](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_compression_types.h#L18-L73)).
     The index entry's accounted size is the plaintext size, which phase 1's `origSize` already
     takes from the attr (`internal/driver/put.go:152`).
  8. **Hints.** INCOMPRESSIBLE (512) on every tail write and on the head's `write_full` of a
     compressed object ([rgw_putobj_processor.cc:146-155](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L146-L155),
     [rgw_rados.cc:3326-3335](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3326-L3335), [librados.h:157](https://github.com/ceph/ceph/blob/v20.2.4/src/include/rados/librados.h#L157)).
  9. **Parts.** Each part compresses on its own, and its record carries its `cs_info`
     ([rgw_putobj_processor.cc:586-591](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L586-L591)), today type none
     (`internal/driver/mp_part.go:183`). UploadPartCopy gets the same through `PutPart`
     (`internal/driver/mp_part.go:291-296`).
  10. **Completion** keeps phase 1's merge, which refuses mixed parts and concatenates the block
      maps ([rgw_sal_rados.cc:4379-4414](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4379-L4414);
      `internal/driver/mp_complete.go:39-60`). POST object compresses through `PutObject`, with
      nothing for BC-32 to add.
- **Specs** (fakerados):
  - PR (a): every phase 1 put suite passes unchanged; the plaintext sinks see the body's bytes;
    the hint steps are as today;
  - PR (b): a 10 MiB PUT into each codec's class, its head and tail layout cut from the
    compressed stream, the manifest's size compressed and the index entry's accounted size
    plaintext; the block map; the 512 hint on every tail write and on the head write; a ranged
    GET of the object through phase 1's reader; a part's record; a mixed upload refused at
    completion by phase 1's merge; the plaintext checks with a body cut short; a failing first
    block stored raw; `random` stored raw;
  - the emulator already records each write's alloc-hint step and carries data and xattrs, so
    the specs need no emulator work.
- **Exclusions and registry:** WD-9's zlib message, and the consequence that an upload with
  parts from both gateways cannot complete under a `compressor_zlib_winsize` narrower than 15
  (the default -15 is unaffected); WD-10's `random`. Phase 1's undocumented difference, a PUT
  into a compressing class stored raw, closes.
- **Depends on:** nothing for PR (a); WP-4 and PR (a) for PR (b). **PR split:** (a) the stage
  split and the hint plumbing; (b) compression. **Brief:** about 230 lines.

### Task WP-11: copies

- **Layer:** driver. **Files:** modify `internal/driver/copy.go`. **Cluster:** no.
- **Consumes:** WP-8, WP-10 (b). **Produces:** `copyData`'s raw flag for C11 (Seams).
- **Behaviour:**
  1. **A streamed copy of an uncompressed source** into a placement with a compression type
     compresses through WP-10's stage, on both releases (D23; Tentacle's `RGWCopyObjDPF`,
     [rgw_op.cc:5825-5884](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5825-L5884)). Squid's radosgw stores it raw.
  2. **A compressed source** stays a raw copy with its codec and attr, whatever the
     destination names (owner decision 4). Tentacle's radosgw re-encodes it.
  3. **The raw flag.** `copyData` (`internal/driver/copy.go:431`) takes a raw flag. True skips
     every filter and keeps the stored compression and checksum attrs, `transition_obj`'s nil
     data-processor factory (WD-11). C11 passes true; a client's CopyObject passes false.
  4. **The checksum attr** follows `set_copy_attrs`: kept under COPY, dropped under REPLACE
     ([rgw_rados.cc:3858-3885](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3858-L3885)). A composite
     checksum is also dropped when the copy streams (WD-6). CopyObject computes and checks no
     checksum, as radosgw's does not (§2.1).
  5. A copy that shares tails (`internal/driver/copy.go:89-103` decides) is unchanged.
- **Specs** (fakerados): the four source and destination combinations, compressed or not into
  a compressing class or not; the raw flag keeping both attrs; the checksum attr kept, dropped
  under REPLACE, and dropped when composite and streamed; UploadPartCopy's decoded source
  recompressed by `PutPart`.
- **Exclusions and registry:** rewrites "A copy that streams its data copies the stored bytes"
  (`docs/exclusions.md:2207`) for D23 and decision 4's remaining half; WD-6's composite drop.
- **Depends on:** WP-8, WP-10 (b). **PR:** one. **Brief:** about 130 lines.

### Task WP-12: checksum rules on reads

- **Layer:** op. **Files:** modify `internal/op/getobject.go`, `objectread.go`, `listparts.go`.
  **Cluster:** no.
- **Consumes:** WP-5, WP-8, WP-9; `ObjectState.Checksum` and `ObjectPart.Checksum`.
  **Produces:** the checksum each read reports, for WP-13 to render.
- **Behaviour:**
  1. **Whether to show one.** GET and HEAD show it when `x-amz-checksum-mode` equals `enabled`
     case-insensitively and there is no Range ([rgw_rest_s3.cc:314-318](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L314-L318),
     [rgw_rest_s3.cc:548-586](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L548-L586)). A record that does not decode shows nothing.
  2. **The whole object** takes `ObjectType` with the parts count; a composite shows
     `<armor>-<parts>`, with `x-amz-checksum-type` beside it.
  3. **A `partNumber` read** takes the part head's record ([rgw_rados.cc:7727-7783](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7727-L7783))
     through `PartType`, bare (WD-5).
  4. **A composite on an object without parts** shows nothing (WD-6).
  5. **GetObjectAttributes**' `Checksum` follows rules 2 and 4. Each part's checksum comes from
     the part head it already stats, never from another part's
     ([rgw_rest_s3.cc:4029-4057](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4029-L4057), [rgw_rest_s3.cc:4083-4096](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4083-L4096),
     [rgw_sal_rados.cc:2908-2925](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2908-L2925)). The registry's "radosgw's
     ObjectParts gives a part without a checksum the checksum of the part before it" is not
     reproduced.
  6. **ListParts** takes `ChecksumAlgorithm` and `ChecksumType` from the upload (WD-7;
     radosgw's [rgw_rest_s3.cc:4695-4698](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4695-L4698)), and each part's checksum.
  7. **Squid** shows what it stores, as phase 1 serves GetObjectAttributes on Squid too.
- **Specs** (fakes): each rule; a Range suppressing the headers; an undecodable record; a CRC
  upload's part read bare with FULL_OBJECT; a composite on a copy without parts; a part without
  a checksum in GetObjectAttributes showing none; ListParts on each release.
- **Exclusions and registry:** retires "Stored checksums are not shown" (`docs/exclusions.md:4013`);
  WD-5, WD-6 and WD-7, and Squid's checksums; the registry entries for defects 5 and 8
  (S3-correctness, unreproduced; WP-14 reproduces them), and the rgw-go line of the
  ObjectParts entry.
- **Depends on:** WP-5, WP-8, WP-9. **PR:** one. **Brief:** about 150 lines.

### Task WP-13: checksum rendering on reads

- **Layer:** S3 handler. **Files:** modify `internal/s3/checksum.go`, `getobject.go`,
  `getobjattrs.go`, `multipart.go`. **Cluster:** no.
- **Consumes:** WP-12, WP-7's helper file. **Produces:** the read responses' checksum headers
  and elements.
- **Behaviour:**
  1. GET and HEAD send `x-amz-checksum-<alg>` or `x-rgw-checksum-<alg>` with WD-8's names, and
     `x-amz-checksum-type`.
  2. GetObjectAttributes' `<Checksum>` holds `Checksum<ALG>` and `ChecksumType`, and each part
     its element ([rgw_rest_s3.cc:4029-4057](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4029-L4057), [rgw_rest_s3.cc:4083-4096](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4083-L4096)).
     Phase 1's empty `<Checksum></Checksum>` (`internal/s3/getobjattrs.go:75-87`) becomes what
     radosgw renders for an absent or undecodable record.
  3. ListParts' elements ([rgw_rest_s3.cc:4695-4713](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4695-L4713)), with WD-7's two.
- **Specs:** bodies byte-compared with xmltext; each header set, per release.
- **Exclusions and registry:** none beyond WP-12's.
- **Depends on:** WP-12, WP-7; V14a by the hot-file order. **PR:** one. **Brief:** about 120
  lines.

### Task WP-14: checksum parity

- **Layer:** integration, recording. **Cluster:** yes.
- **Files:** the T unit's excluded-markers list (the `checksum` line); `hack/s3tests/deselect-phase2.txt`;
  both s3-tests ledgers; `test/s3tests/known-differences-{squid,tentacle}.txt`; WP-1's
  testdata; `docs/ceph-upstream-bugs.md`.
- **Behaviour:**
  1. Delete `deselect-phase2.txt:130-131` and the `checksum` marker, freeing the 16 tests of
     §5.1 and test_get_checksum_object_attributes. test_multipart_sse_c_checksum_complete stays
     out under `encryption` (phase 4).
  2. test_post_object_upload_checksum goes into the POST section of the deselect list until
     BC-34 lands, and BC-34 deletes it.
  3. Record both ledgers for the freed tests with `make s3tests-record-tests`, the gate set
     (T-1), before any comparison.
  4. Write the known differences §5.1 predicts, each under its exclusion, once the recording
     confirms them on both releases. With D21 they are:
     - Squid: the single-object tests rgw-go passes and Squid's radosgw fails
       (test_object_checksum_sha256, _crc64nvme, _sha512, test_get_checksum_object_attributes),
       under D22's exclusion;
     - Tentacle: test_object_checksum_sha512 (WD-8) and the five
       test_multipart_use_cksum_helper_* (WD-5).
     The md5, xxhash64, xxhash128 and xxhash3 tests fail on both and need no line.
  5. Say from the run's `.freeze` whether botocore sends a checksum on every PUT and asks for
     checksum mode on every GET. If it does, the checksum code runs across the whole set; the
     PR description and the T unit's harness notes say so.
  6. Capture `user.rgw.cksum` from radosgw-written Tentacle objects, xxh3 and blake3 records
     among them, and parts' and combined records, as WP-1's goldens.
  7. Reproduce defects 1 to 8 on Tentacle, record each result in the registry with its release,
     and run the prior-art search before any filing (Global Constraints).
- **Specs:** the ledger checks pass; `make s3tests-parity` on each release has no unexpected
  difference; WP-1's suite passes with the captured goldens.
- **Depends on:** WP-6 to WP-13; WP-22 and T-1 (the lists and the sets); BC-34, or the POST
  line deselected.
- **PR split:** (a) the lists, ledgers and known differences, landed by the controller with
  the other recordings, one at a time; (b) the goldens into WP-1's suite; (c) the registry's
  reproductions. **Brief:** about 120 lines.

### Task WP-15: compression and checksum gate

- **Layer:** integration. **Files:** create `test/integration/phase2_writepath_test.go`,
  labelled `integration, rgw-go, phase2, writepath`. **Cluster:** yes.
- **Behaviour:** the report's §4 gate cases 1 to 5 on both releases, the Tentacle-only cases
  skipped on Squid. "A→B" means A writes and B reads; every row runs both ways unless marked.
  radosgw's view is read through `radosgw-admin object stat`, and `radosgw-admin bucket check`
  must come back clean after each case.
  1. **Compression.** Through each codec's class (`populate.sh` makes one per codec on STANDARD's
     pool), a 10 MiB PUT is read by the other gateway whole, by ranges inside and across blocks,
     and through `object stat`. The stored block map is radosgw's shape: one block per chunk,
     back to back. rgw-go→radosgw is new; radosgw→rgw-go is phase 1's read gate.
  2. **Compressed multipart.** One upload into a compressing class, with parts from both
     gateways, is completed by each and read by both.
  3. **Checksums, Tentacle.**
     - PutObject with each served algorithm, in header and trailer form, then HEAD with
       checksum mode and GetObjectAttributes through the other gateway.
     - A COMPOSITE SHA256 upload and a FULL_OBJECT CRC32 upload, each begun on one gateway with
       parts through both, completed on the other.
     - A retried completion answered by the gateway that did not complete it.
     - `object stat` shows the same `user.rgw.cksum` bytes for the same input either way.
       Defects 2 to 4's cases are left out of that comparison and pinned separately.
  4. **Checksums, Squid.** rgw-go's checksummed PUT is read whole by Squid's radosgw, and a
     Squid radosgw COPY keeps the attr.
  5. **Copy.** rgw-go streams a radosgw-compressed source, and Tentacle's radosgw copies an
     rgw-go-compressed object. Both read back byte-exact through both gateways.
  6. **Review Focus 4, one way.** A PUT to rgw-go naming MD5, XXHASH64 or XXHASH128 answers 400
     and stores nothing: radosgw's HEAD of the key is 404, and `bucket check` is clean.
  7. **Compression stats.** S3 shows no trace of compression: HEAD and GET give the plaintext's
     size, so the admin stats are the only client-visible witness. The same compressible body is
     written by each gateway through each codec's class, once by PutObject and once as a two-part
     upload, each object into a bucket of its own: one bucket per gateway, codec and mode, holding
     that one object. The codecs' encoders differ, so the stored bytes may too; the two gateways'
     objects compare as follows:
     - `radosgw-admin bucket stats`: `size`, the accounted size, the plaintext's, is equal, and so
       is `size_actual`, that size rounded up to 4 KiB per object by `cls_rgw_get_rounded_size`
       ([cls_rgw.cc:1183-1185](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1183-L1185),
       [cls_rgw.cc:1316-1318](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1316-L1318);
       [cls_rgw_types.h:125-128](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L125-L128),
       [cls_rgw_types.h:128-131](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L128-L131)).
       Each bucket's `size_utilized`, the stored size, is below its `size` and, as the bucket
       holds one object, equals the end of that object's block map, its last block's `new_ofs`
       plus `len`
       ([rgw_rados.cc:5540-5555](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5540-L5555),
       [rgw_rados.cc:6196-6211](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6196-L6211);
       [rgw_common.cc:3108-3121](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L3108-L3121),
       [rgw_common.cc:3164-3177](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L3164-L3177));
     - `object stat`'s `compression` section, the `user.rgw.compression` attr it decodes as an
       `RGWCompressionInfo`
       ([rgw_admin.cc:8611-8612](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L8611-L8612),
       [radosgw-admin.cc:9045-9046](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/radosgw-admin/radosgw-admin.cc#L9045-L9046)):
       the same `compression_type`, `orig_size`, `compressor_message` and blocks' `old_ofs`,
       while each block's `new_ofs` and `len` are its writer's own.

     Optionally, one s3-tests selection run with STANDARD compressing, as radosgw's own
     rgw/verify QA runs s3-tests under a `compression type` that `configure_compression` sets on
     the default placement
     ([overrides.yaml:20](https://github.com/ceph/ceph/blob/v20.2.4/qa/suites/rgw/verify/overrides.yaml#L20),
     [rgw.py:374-384](https://github.com/ceph/ceph/blob/v20.2.4/qa/tasks/rgw.py#L374-L384)). That QA
     sets `random`, which rgw-go stores uncompressed (WD-10), so the run names a codec instead.
- **Depends on:** WP-8 to WP-11, WP-13. **PR:** one. **Brief:** about 160 lines.

### Task WP-16: the otp class client

- **Layer:** class client. **Files:** create `internal/cls/otp/{doc.go,types.go,ops.go}` and its
  suite. **Cluster:** no.
- **Consumes:** `denc`, `radosclient`, `meta.UserID`. **Produces** (Seams), in the pattern of
  `internal/cls/version/ops.go:21,53`:
  - `OTPInfo` (`otp_info_t` v1) and its JSON; `OTPCheck` (`otp_check_t`); the check-result
    constants;
  - `OTPObject(uid meta.UserID) string`;
  - `Set(op radosclient.Execer, devs []OTPInfo, r denc.Release)`, an `otp_set` exec;
  - `GetAll(op *radosclient.ReadOp, r denc.Release) *GetResult`, `otp_get` with `get_all`, and
    `(*GetResult).Devices() ([]OTPInfo, error)`;
  - `Remove(op radosclient.Execer, ids []string, r denc.Release)`;
  - `GetCurrentTime(op *radosclient.ReadOp, r denc.Release) *TimeResult`.
- **Behaviour:**
  1. **The wire types,** all v1 ([cls_otp_types.h:31-77](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.h#L31-L77),
     [cls_otp_ops.h:11-167](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_ops.h#L11-L167)): `otp_info_t` is u8 type (TOTP 2), id, seed, u8
     seed type (hex 1, base32 2), bufferlist `seed_bin`, i32 `time_ofs`, u32 `step_size`
     (default 30), u32 `window` (default 2). The set, get, reply and remove ops sit beside it.
  2. **The calls** are [cls_otp_client.cc:33-188](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L33-L188), identical at v20.2.4 but for
     `otp_check`, which V13a owns. Method flags: `otp_set` RD|WR, `otp_get` RD, `otp_remove`
     RD|WR, `get_current_time` RD ([cls_otp.cc:558-575](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L558-L575)).
  3. **The JSON** is `dump` and `decode_json`: type as an int, id, seed, `seed_type` as `hex`,
     `base32` or `unknown`, `time_ofs`, `step_size`, `window`. `seed_bin` is never shown
     ([cls_otp_types.cc:30-71](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.cc#L30-L71)).
  4. **The object** is `"user:" + uid.String()` in the zone's `otp_pool`
     ([svc_cls.h:56-58](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.h#L56-L58), [svc_cls.h:56-58](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_cls.h#L56-L58)).
- **Specs:** request bytes for each call, from corpus goldens where `ceph-object-corpus` has
  the type and hand-built from the encoders otherwise; reply decoding; the JSON round trip,
  `seed_bin` never shown; `OTPObject` for a plain and a tenant user.
- **Exclusions and registry:** none.
- **Depends on:** nothing. **PR:** one. **Brief:** about 120 lines.

### Task WP-17: the otp class emulator

- **Layer:** emulator. **Files:** create `internal/testutil/fakerados/cls_otp.go` and its
  specs. **Cluster:** no.
- **Consumes:** WP-16's types. **Produces:** `OTPClassAt(now func() time.Time) ClassFunc`,
  `OTPClass()` as `OTPClassAt(time.Now)`, `OTPWriteMethods` derived from the table's write flags,
  and the empty `otpCheckMethods` table V13b fills in `cls_otp_check.go` (Seams).
- **Behaviour:**
  1. **Dispatch** is a method table, `name -> (handler, write flag)`, merged from
     `otpAdminMethods` and `otpCheckMethods`, as S1 does for the rgw class. A name in both
     panics at assembly; an unknown method is EOPNOTSUPP.
  2. **Storage** is the class's: omap `header` holds `otp_header{set<id>}` v1, and `otp/<id>`
     holds `otp_instance{otp_info_t, list<otp_check_t> last_checks, u64 last_success}` v1
     ([cls_otp.cc:45-90](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L45-L90)). The instance type keeps `last_checks` and
     `last_success` for V13b's methods.
  3. **`otp_set`** reads each id's existing instance, so its replay state survives, replaces
     its `otp_info_t`, parses the seed by its type, writes the instance and records the id in
     `header` ([cls_otp.cc:268-348](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L268-L348)).
  4. **`otp_get`** with `get_all` takes the ids from `header` and returns the entries found
     ([cls_otp.cc:396-445](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L396-L445)). **`otp_remove`** and **`get_current_time`**
     follow the same file.
  5. **Errors:** EINVAL for a request that does not decode and for a seed that does not parse;
     a missing object answers as the class does ([cls_otp.cc:268-445](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L268-L445)).
  6. **Formatting** follows WD-16.
- **Specs:** each method; a set that preserves a device's replay state; `get_all` against named
  ids; each EINVAL; the write list; a duplicate name panicking.
- **Exclusions and registry:** none.
- **Depends on:** WP-16, S1, S4. **PR:** one. **Brief:** about 140 lines.

### Task WP-18: metadata sections as tables

- **Layer:** driver (a), op (b). **Cluster:** no.
- **Files:** (a) `internal/driver/metadata.go`, and one file per existing section,
  `metadata_user.go`, `metadata_bucket.go`, `metadata_instance.go`; (b)
  `internal/op/adminmetadata.go`, `op.MetadataStore`, memstore and the fakes.
- **Consumes:** phase 1's sections. **Produces:**
  - (a) a driver section interface, the get, put, remove and listing (pool and key mapping)
    each section has, registered by name; today's three switches
    (`internal/driver/metadata.go:92-106`, `internal/driver/metadata.go:738-776`, and those of
    `Put` and `Remove`) become lookups;
  - (b) `MetadataStore.Sections() []string`, with `metadataSections`
    (`internal/op/adminmetadata.go:31`) derived from it in std::set order; and the put status
    `UpdateStatus` names per section (`internal/op/adminmetadata.go:209-224`) as a per-section
    rule, given whether the put applied and the release, plus whether `RGWX_UPDATE_VERSION`
    reports the version read.
- **Behaviour:** unchanged.
- **Specs:** every phase 1 metadata spec passes unchanged; an unknown section stays ErrNoSuchKey
  before the body is parsed; the derived list equals today's.
- **Exclusions and registry:** none.
- **Depends on:** nothing. **PR split:** (a) the driver's table; (b) the op's. **Brief:** about
  100 lines.

### Task WP-19: the otp metadata section

- **Layer:** driver, with one row in each of WP-18's tables. **Files:** create
  `internal/driver/metadata_otp.go`; add `otp_header` v1's encoding to `internal/cls/otp`
  (`types.go`), with a hand-built golden byte-identical to the class's, since fakerados's
  encoder is test-only; one row in the driver's section table and one in the op's status
  table. **Cluster:** no.
- **Consumes:** WP-16, WP-17 (specs), WP-18; `meta.ZoneParams.OTPPool`. **Produces:** the `otp`
  section.
- **Behaviour** (§2.5):
  1. **Keys.** The section's key is the object name `user:<uid>`, with no prefix
     ([svc_otp.cc:15-47](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_otp.cc#L15-L47)); its metadata key is `otp:user:<uid>`
     ([svc_meta_be_otp.cc:20-23](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_meta_be_otp.cc#L20-L23), [rgw_otp.cc:145-148](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L145-L148)).
  2. **get** is one read op: stat for the mtime, a cls_version read, and `otp_get` with `get_all`
     ([svc_cls.cc:218-245](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L218-L245)). It answers `{"devices": [...]}`
     ([rgw_otp.cc:47-49](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_otp.cc#L47-L49), [rgw_otp.cc:24-26](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L24-L26)).
     A missing object is ErrNoSuchKey.
  3. **put** is one write op ([svc_cls.cc:65-87](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L65-L87),
     [svc_cls.cc:188-216](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L188-L216)):
     - remove with FAILOK, then create(false);
     - the version set: the document's `ver` when its tag is non-empty, else a fresh random tag at
       version 1;
     - `mtime2`, the document's mtime;
     - `otp_set` with the device list;
     - then WD-17's raw write of `header`, holding exactly the document's ids.
     `otp_set` reads the stored records across the remove, so every device the document keeps
     keeps its replay state, as radosgw's put does (WP-17). The header write leaves no id of a
     dropped device, which radosgw's put keeps (defect 14).
  4. **Refusals** before any write, 400 InvalidArgument: WD-13's four, and WD-15's seed bound,
     every device checked first. A string member given null, an object or an array is refused
     too, as WP-16's `OTPInfo` decodes the device, in the idiom of the sections already served.
  5. **update-type and the status** follow WD-14; `RGWX_UPDATE_VERSION` reports `ver:0,tag:`
     through WP-18 (b)'s rule.
  6. **remove** reads, then removes under the version read ([rgw_metadata.cc:554-573](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_metadata.cc#L554-L573),
     [rgw_otp.cc:84-92](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L84-L92)).
  7. **list** lists every object of `OTPPool`, each key its object name
     ([rgw_otp.cc:110-138](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L110-L138)).
  8. **No cache notify**, as radosgw sends none for otp (§2.6). A put changes no user record: MFA
     through the metadata API also needs the user's `mfa_ids`, which the user section already
     carries ([rgw_admin.cc:10791-10856](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L10791-L10856)).
  9. **Errors.** The put's body goes through `decodeMetadataPut`, which replaces a syntax error
     with its own: encoding/json's quotes the document, so it can quote a seed (WP-16's godoc).
     No otp decode error is logged in full.
  10. **Formatting** follows WD-16.
- **Specs** (fakerados with WP-17): each verb; each refusal, with nothing written; a seed of
  exactly 1024 bytes accepted, one of 1025 refused, and a second device over the bound writing
  nothing; a string member given null, an object or an array refused; the release split of the
  put status and headers; `update-type` honoured; a list across pages; a put keeping a
  remaining device's replay state; a put that drops a device leaving `header` exactly the
  document's ids, in the class's bytes, and `get_all` answering the remaining devices; the
  document round trip.
- **Exclusions and registry:**
  - rewrites "The admin metadata API serves three sections" (`docs/exclusions.md:1646`): four,
    and otp's `update-type` and refusals, WD-15's seed bound beside WD-13's, saying only writes
    are bounded, so a longer seed radosgw wrote still reads back;
  - names the otp section in "The metadata sections write only what keeps a user or bucket
    whole", among the documents whose string members refuse null, an object or an array;
  - updates the rgw-go line of the registry's "cls_otp's otp_set puts an array as long as the
    seed on the OSD's stack" to WD-15's bound;
  - updates the rgw-go lines of the registry's "cls_otp divides by a stored step_size that is
    never validated" (#80948) and "cls_otp computes the replay index from an unsigned window
    distance" (#80949) to say what the section now writes;
  - WD-17's exclusion: rgw-go's otp put leaves `header` listing exactly the document's devices,
    where radosgw's keeps a dropped device's id;
  - updates the rgw-go line of the registry's "radosgw's otp metadata put keeps a dropped
    device's id in the otp header" (defect 14) to WD-17's header write;
  - updates the rgw-go line of the registry's "radosgw's otp replay state stays in the zone
    that recorded it" (defect 12) to say the section's document carries only the devices, as
    radosgw's does.
- **Depends on:** WP-16, WP-17, WP-18. **PR:** one. **Brief:** about 190 lines.

### Task WP-20: object layout

- **Layer:** driver, with its op seam. **Files:** create `internal/driver/layout.go`; the
  `LayoutStore` interface in `internal/op/layout.go`, with memstore and the fakes.
  **Cluster:** no.
- **Consumes:** `meta.Manifest.MarshalJSON`, the driver's head ref. **Produces:**
  the implementation of S2's `LayoutStore.ObjectLayout(ctx context.Context, st *ObjectState) (json.RawMessage, error)`,
  kept off `ObjectStore` so the hot interface is not touched.
- **Behaviour:** the `result` document of `RadosObject::dump_obj_layout`
  ([rgw_sal_rados.cc:2780-2826](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2780-L2826),
  [rgw_sal_rados.cc:3615-3661](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3615-L3661)):
  1. `head`, an `rgw_raw_obj` of `{pool, oid, loc}` from the head ref
     ([rgw_common.cc:3137-3142](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L3137-L3142), [rgw_common.cc:3193-3198](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L3193-L3198));
  2. `manifest`, `RGWObjManifest::dump`. **The tags differ:** `tier_config` prints for
     `cloud-s3` only at v19.2.6, and for every supported tier type at v20.2.4
     (`git diff v19.2.6 v20.2.4 -- src/rgw/driver/rados/rgw_obj_manifest.cc`). Phase 1's
     `MarshalJSON` renders v20.2.4's; Squid takes v19.2.6's rule;
  3. `data_location`, one `{ofs, loc, loc_ofs, loc_size}` per manifest stripe, `loc_size` the
     stripe size capped at what is left of the object.
  The bytes are ceph's JSONFormatter's: keys in this order, no spaces, and no HTML escaping.
- **Specs:** documents for the corpus' shapes, in-head, striped, multipart, compressed and an
  `_underscore` key, derived by hand from the rules above; WP-24 adds radosgw's captured bodies
  to this suite as goldens; Squid's and Tentacle's `tier_config` rule on a cloud-tier manifest.
- **Exclusions and registry:** none.
- **Depends on:** nothing. **PR:** one. **Brief:** about 140 lines.

### Task WP-21: GET `?layout`

- **Layer:** op and S3 handler. **Files:** create the op in `internal/op/layout.go`; bind the
  route in S2's `internal/s3/layout.go`. **Cluster:** no.
- **Consumes:** WP-20, S2, V6. **Produces:** RGWGetObjLayout (D25).
- **Behaviour:**
  1. **Dispatch.** An object GET `?layout`, after `acl` and `uploadId` and before `tagging`
     ([rgw_rest_s3.cc:4835-4843](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4835-L4843), [rgw_rest_s3.cc:5360-5367](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5360-L5367)).
     Phase 1's dispatch row exists (`internal/s3/dispatch.go:207`). HEAD has no such branch, so
     HEAD `?layout` stays a HEAD object.
  2. **The object** is read first, as for every object op: a missing object follows the existing
     missing-object rule before the cap check. V6 resolves the current version, or the one
     `versionId` names.
  3. **Permission.** Name `get_obj_layout`, op mask READ. VerifyPermission is
     `CheckCaps(r, "admin", read)` alone, with no ACL or policy evaluated
     ([rgw_op.h:2537-2554](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L2537-L2554), [rgw_op.cc:7904-7911](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L7904-L7911),
     [rgw_op.cc:8809-8816](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L8809-L8816)). The bucket owner without the cap is refused.
  4. **The response.** 200 `application/json`, `{"result": <WP-20's document>}`, with a
     Content-Length of the body and no Accept-Ranges ([rgw_rest_s3.cc:4368-4385](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4368-L4385),
     [rgw_rest_s3.cc:4879-4896](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4879-L4896), [rgw_client_io_filters.h:208-236](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_client_io_filters.h#L208-L236)).
  5. **A layout error** is answered as an error (WD-12).
- **Specs:** the cap refusal, the bucket owner included; a missing object before the cap check;
  the body of each shape; a versioned key's current version and a `versionId`; HEAD `?layout`;
  a layout error.
- **Exclusions and registry:** removes `?layout`'s 501 from "Tentacle's x-amz-delete-marker is
  false, or absent, on a missing object" (`docs/exclusions.md:4127-4132`); WD-12's exclusion; the
  registry entry for defect 10 (cosmetic, unreproduced; WP-24 attempts it).
- **Review:** adversarial (an authorization check).
- **Depends on:** WP-20, S2, V6. **PR:** one. **Brief:** about 110 lines.

### Task WP-24: `?layout` and otp gate

- **Layer:** integration. **Files:** create `test/integration/phase2_layout_otp_test.go`,
  labelled `integration, rgw-go, phase2, layout, otp`. **Cluster:** yes.
- **Behaviour:** the report's §4 gate cases 6 and 7, on both releases.
  1. **`?layout`, one way.** For every populated object and for rgw-go-written in-head, striped,
     multipart and compressed ones, rgw-go's and radosgw's bodies are byte-identical. The
     captured bodies become WP-20's goldens.
  2. **otp.**
     - `radosgw-admin mfa create`, then rgw-go's `GET /admin/metadata/otp?key=user:<uid>` shows
       the device.
     - rgw-go's metadata put, with the user's `mfa_ids` through the user section, is then listed
       by `radosgw-admin mfa list` and accepted by `radosgw-admin mfa check` with a TOTP the spec
       computes from the seed (RFC 6238, the device's step and offset).
     - Metadata list and remove through each gateway.
  3. **Reproductions.**
     - Replay state across a put: a TOTP accepted, the device put again through either
       gateway's metadata API, and the same TOTP refused inside its window, since the put keeps
       the replay state (WP-17).
     - Defect 14: a put through radosgw's metadata API that drops a device, after which
       radosgw's metadata get answers 404 and `radosgw-admin mfa list` prints no devices; the
       same put through rgw-go leaves both listing the remaining devices.
       `radosgw-admin mfa remove` of the dropped id restores radosgw's object.
     - Defect 12 needs two zones, so its entry stays unreproduced, with that reason.
     - Defect 10: an input that drives `dump_obj_layout` into its error path on a running
       release, if one can be built without corrupting shared state; otherwise the entry stays
       unreproduced, with the reason.
     Each result goes into the registry with its release.
- **Depends on:** WP-19, WP-21. **PR split:** (a) the gate cases and goldens; (b) the registry's
  reproductions. **Brief:** about 120 lines.

## Order

Everything starts after the index's deferred benchmark run and the shared tasks it names.

| Wave | Tasks, in parallel | Waits for |
|---|---|---|
| 1 | WP-1, WP-3, WP-4, WP-10 (a), WP-16, WP-18 (a), WP-20 | nothing |
| 2 | WP-2, WP-10 (b), WP-17, WP-18 (b) | WP-1; WP-4 and WP-10 (a); WP-16; WP-18 (a) |
| 3 | WP-5, WP-19, WP-21 | WP-2; WP-17 and WP-18; WP-20, S2 and V6 |
| 4 | WP-6, WP-8 | WP-3 and WP-5; WP-5 and WP-10 (b) |
| 5 | WP-7, WP-9, WP-11 | WP-6, V11 and V14a; WP-8; WP-8 and WP-10 (b) |
| 6 | WP-12, then WP-13 | WP-9; WP-12, WP-7 and V14a |
| cluster, one at a time | WP-24, WP-15, WP-14 | WP-19 and WP-21; WP-8 to WP-11 and WP-13; WP-6 to WP-13, WP-22, T-1, and BC-34 or the POST line deselected |

- Wave 1 is wider than the subagent budget allows at once; the controller starts WP-10 (a),
  WP-1 and WP-16, which unblock the most.
- The cluster tasks run one at a time across the whole plan, and WP-14 (a) lands with the other
  units' recordings through the controller.
- Cross-unit order this unit imposes:
  - BC-26 lands after WP-6 and WP-7 on the files they share, and after WP-13 for its PR (b);
  - V13a and V13b follow WP-16 and WP-17;
  - C11 follows WP-11;
  - L5a follows WP-5 on `internal/op/multipart.go`.

## Defects

The report's §6 table, carried with class and status. Every entry is new and unreproduced,
derived from the source at v20.2.4 (Squid has no checksum code) unless the row says otherwise.
The task named last in a row budgets the cluster reproduction (PP-11) and runs the prior-art
search before any filing.

| # | Defect | Class | Status | rgw-go's choice | Tasks |
|---|---|---|---|---|---|
| 1 | CompleteMultipartUpload combines the wrong parts: `try_sum_part_cksums` pages `list_parts` by the request's part count and keeps only the last page ([rgw_op.cc:7068-7092](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7068-L7092), [rgw_sal_rados.cc:4193](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4193)), then combines every part of it. A FULL_OBJECT CRC that SDKs validate on GET then fails the download. | S3-correctness (integrity metadata wrong; data intact) | unreproduced | WD-4: exactly the named parts | WP-9, WP-14 |
| 2 | `CombinerFactory` ignores the upload's flags ([rgw_cksum.cc:145-155](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.cc#L145-L155)), so a CRC upload created COMPOSITE gets a full-object CRC. Also on main at the pin ([rgw_cksum.cc:147-157](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_cksum.cc#L147-L157)). | S3-correctness | unreproduced | WD-1: the composite | WP-2, WP-9, WP-14 |
| 3 | A single-part CRC upload is reported COMPOSITE: its combined record keeps the part's flags without V2 ([rgw_cksum_pipe.h:187-191](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L187-L191), [rgw_cksum.cc:129-142](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.cc#L129-L142)). | S3-correctness | unreproduced | WD-1: `V2\|COMBINED\|FULL_OBJECT` | WP-2, WP-14 |
| 4 | Two CRC64NVME checks are skipped: completion's supplied value is never compared ([rgw_cksum_pipe.h:120-136](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L120-L136)), nor a part's value sent without an algorithm header ([rgw_cksum_pipe.h:33-63](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L33-L63)). main adds no case (to verify in WP-14). | S3-correctness (integrity check skipped) | unreproduced | compare for every type (WD-4, rule 7 of WP-6) | WP-6, WP-9, WP-14 |
| 5 | A `partNumber` GET with checksum mode renders the part's checksum as `<armor>-<parts>` with COMPOSITE ([rgw_rest_s3.cc:548-586](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L548-L586)). main renders it bare ([rgw_rest_s3.cc:569-608](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_rest_s3.cc#L569-L608)), and s3-tests expect that. | S3-correctness | unreproduced | WD-5: main's rule; known differences | WP-12, WP-13, WP-14 |
| 6 | A checksum value without an algorithm header is stored without any checksum and never verified ([rgw_cksum_pipe.cc:46-72](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.cc#L46-L72)). | S3-correctness (integrity check skipped) | unreproduced | WD-2: infer and verify | WP-6, WP-14 |
| 7 | Completion accepts a supplied checksum that only starts with the computed one ([rgw_op.cc:7317-7334](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7317-L7334)). | S3-correctness, minor | unreproduced | WD-4: exact, or a composite without `-N` | WP-9, WP-14 |
| 8 | ListParts never reports `ChecksumAlgorithm`: it reads the meta object's `user.rgw.cksum`, which nothing writes ([rgw_rest_s3.cc:4695-4698](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4695-L4698), [rgw_op.cc:7617-7630](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7617-L7630)). | S3-correctness, minor | unreproduced | WD-7: from `multipart_upload_info` | WP-12, WP-13, WP-14 |
| 9 | `Cksum::decode` copies a stored digest size of up to 65535 bytes into a 64-byte array ([rgw_cksum.h:268-281](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L268-L281)), and a v20.2.4 gateway indexes its nine-entry table with any stored type ([rgw_cksum.h:147-149](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L147-L149)), which main's types 9 to 11 overrun in a mixed v20 and v21 zone. | security (memory safety), privileged: a RADOS writer of the attr, or a newer radosgw | unreproduced | refuse both on read, never write either (D21, Review Focus 4); no exclusion; sent to the rgw-bug-reproduction session | WP-1, WP-6, WP-8 |
| 10 | `?layout` ignores `dump_obj_layout`'s error and answers 200 with a truncated `{"result":{...}}` ([rgw_rest_s3.cc:4879-4896](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4879-L4896), [rgw_rest_s3.cc:4368-4385](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4368-L4385)). Both tags. | cosmetic | unreproduced | WD-12: answer the error | WP-21, WP-24 |
| 11 | An otp put with `step_size` 0 makes every later check fault the OSD on a divide (registry, "cls_otp divides by a stored step_size that is never validated", #80948). Both tags. | security (DoS, admin-only) | reproduced in userspace only (registry) | WD-13: refuse it; update the registry's rgw-go line | WP-19 |
| 12 | A device's replay state, `last_checks` and `last_success`, lives in each zone's otp object and is never synced. The metadata document holds only `devices` ([rgw_otp.cc:47-49](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_otp.cc#L47-L49), [rgw_otp.cc:24-26](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L24-L26)), each an `otp_info_t` ([cls_otp_types.cc:30-71](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.cc#L30-L71), [cls_otp_types.cc:30-71](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp_types.cc#L30-L71)), `otp_get` answers only the device ([cls_otp.cc:439](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L439), [cls_otp.cc:439](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L439)), and a check, which reads the local zone's object, writes no metadata log entry ([svc_cls.cc:32-63](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L32-L63), [svc_cls.cc:32-63](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_cls.cc#L32-L63)). So a TOTP accepted at one zone is accepted once more at each other zone inside its window, which RFC 6238 section 5.2 forbids (registry, "radosgw's otp replay state stays in the zone that recorded it"). Both tags. | security (possible), low: a replay needs the user's credentials and an observed code | unreproduced: it needs two zones | keep it: the stored form, the document and the sync are radosgw's; WP-19 updates the entry's rgw-go line | WP-19 |
| 13 | `otp_set`'s `parse_seed` declares a stack array as long as each seed, which nothing bounds below the OSD's 90 MiB write limit (registry, "cls_otp's otp_set puts an array as long as the seed on the OSD's stack"). Both tags; fixed on main, in v21.0.1 and later. | security (possible): DoS, admin-only | unreproduced | WD-15: refuse a seed longer than 1024 bytes; update the registry's rgw-go line | WP-19 |
| 14 | An otp put that drops a device keeps its id in `header`. The put removes and recreates the object and calls `otp_set` in one op, and `otp_set` reads the committed header ([svc_cls.cc:188-216](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L188-L216), [svc_cls.cc:188-216](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_cls.cc#L188-L216); [cls_otp.cc:301-348](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L301-L348), [cls_otp.cc:301-348](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L301-L348); registry, "radosgw's otp metadata put keeps a dropped device's id in the otp header"). Every later `get_all` fails with ENOENT, and metadata sync then removes the user's otp object on the peer zones. Both tags. | data corruption | unreproduced | WD-17: the put's raw `header` write; exclusion | WP-17, WP-19, WP-24 |

Not defects, recorded so no task re-derives them:
- `?layout` checks only the `admin` cap, by design.
- An otp put does not reset a device's replay state: `otp_set` reads the stored records across
  the op's remove ([PrimaryLogPG.cc:7779-7800](https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7779-L7800), [PrimaryLogPG.cc:7856-7877](https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7856-L7877)). The report's row 12 assumed it did.
- An otp put changes no user record; `radosgw-admin mfa create` writes both the device and the
  user's `mfa_ids` ([rgw_admin.cc:10791-10856](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L10791-L10856)).
- An algorithm header with no value header and no trailer stores the computed checksum
  unverified, as the AWS SDKs' flow expects.
- RGWEnv's map compares keys case-insensitively ([rgw_common.h:494](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L494)), so a
  lower-case algorithm value still finds its value header.

Existing registry entries this unit meets:
- "radosgw's ObjectParts gives a part without a checksum the checksum of the part before it":
  not reproduced (WP-12).
- "compressor_zlib_winsize 8 writes zlib blocks radosgw cannot read back": WP-4 writes such a
  winsize's blocks with message 15 (WD-9).
