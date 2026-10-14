# Phase 2 research: writepath

Area: checksums at Tentacle level, compression on the write path, placement on the write path,
the `?layout` extension, the admin otp metadata section, and the phase gate's tooling deltas.

## 1. Scope

Spec §9, phase 2, in its words: "checksums at Tentacle level, compression and placement on the
write path, … the `?layout` extension. Admin adds the otp metadata section." Spec §8 fixes the
release rule this area leans on: "a Tentacle gateway-side feature is available on a Squid
cluster when its persistent form is a separate attribute, as checksums are, and unavailable when
it is a field of a struct the cluster's radosgw would re-encode without it".

| Item | S3 and admin surface |
|---|---|
| Checksums at Tentacle level | PutObject and UploadPart: `x-amz-checksum-algorithm` / `x-amz-sdk-checksum-algorithm`, `x-amz-checksum-<alg>` header, the same value as an aws-chunked trailer (`x-amz-trailer`), and the response's `x-amz-checksum-<alg>` and `x-amz-checksum-type`; UploadPartCopy's part checksum; CreateMultipartUpload's algorithm and `x-amz-checksum-type` (COMPOSITE or FULL_OBJECT) and its `x-amz-checksum-algorithm` response header; CompleteMultipartUpload's supplied `x-amz-checksum-<alg>`, the combined checksum (composite digest of part digests with `-N`, or a combined full-object CRC) and its `Checksum<ALG>`/`ChecksumType` elements; GET and HEAD with `x-amz-checksum-mode: enabled` (whole object and `partNumber`); GetObjectAttributes' `Checksum` and each `ObjectParts` part's checksum; ListParts' `ChecksumAlgorithm` and per-part checksum; POST object's `x-amz-checksum-<alg>` form field (bucketconfig consumes the seam); the stored form `user.rgw.cksum`, `multipart_upload_info`'s type and flags, and `RGWUploadPartInfo.cksum`. |
| Compression on the write path | The zone placement's per-storage-class `compression_type` applied to PutObject, UploadPart (and UploadPartCopy, which runs the same writer), POST object, and a CopyObject that streams plaintext; the block layout (one block per `rgw_max_chunk_size` read), each codec's block framing and `compressor_message`, `user.rgw.compression` (`RGWCompressionInfo`) on heads and in part records, the incompressible allocation hint, and the interaction with ranges (read path, phase 1), multipart (part records and completion), copy (owner decision 4) and lifecycle transition (raw copy). |
| Placement on the write path | Already served by phase 1 for the bucket's placement rule, `x-amz-storage-class`, head and tail pools and storage-class labels (§3). Phase 2's remainder is the compression type the placement's storage class names, Tentacle's recompressing copy, and the placement seam lifecycle's transition consumes. |
| `?layout` | `GET /{bucket}/{key}?layout`: radosgw's `RGWGetObjLayout`, an **object** GET returning the head, manifest and data locations as JSON to a user with the `admin=read` cap. It is not a bucket index layout: no bucket-scope `layout` key exists at either tag (§2.4). |
| Admin otp metadata section | `GET /admin/metadata/otp` (list), `GET`, `PUT` and `DELETE /admin/metadata/otp?key=user:<uid>`: the `{"devices": [otp_info_t...]}` document over cls_otp `otp_get`/`otp_set` on `user:<uid>` in the zone's `otp_pool`, plus the `cls/otp` package and emulator that versioning's MFA check extends. |
| Phase-gate tooling deltas | `hack/s3tests/run.sh`'s marker set and the include list for marker-excluded tests the phase serves; `deselect-phase2.txt`'s checksum line; the radosgw ledgers for the freed tests; the go-ceph rgw/admin suite (no entry in this area); the shared-zone gate's write/read cases for compressed and checksummed objects, `?layout` and otp. |

## 2. radosgw's behaviour

Squid has no checksum code at all: v19.2.6 has no `rgw_cksum*` or `rgw_crc_digest.h` file and
no GetObjectAttributes, and its only checksum references are the aws-chunked trailer framing.
Every checksum behaviour below is v20.2.4's; where this section says "both tags" it means the
code is identical at v19.2.6 and v20.2.4.

### 2.1 Checksums (v20.2.4)

**Types and stored form.**
- `rgw::cksum::Type` is a u16: none 0, crc32 1, crc32c 2, xxh3 3, sha1 4, sha256 5, sha512 6,
  blake3 7, crc64nvme 8 (`src/rgw/rgw_cksum.h:41-52@v20.2.4`). Digest sizes 4, 4, 8, 20, 32,
  64, 32, 8. Only crc32, crc32c, sha1, sha256 and crc64nvme are AWS types; crc32, crc32c and
  crc64nvme are CRCs (`src/rgw/rgw_cksum.h:88-99@v20.2.4`).
- An AWS type's header is `x-amz-checksum-<name>`; any other's is `x-rgw-checksum-<name>`; the
  XML element is `Checksum<NAME>` (`src/rgw/rgw_cksum.h:177-192@v20.2.4`). The value is the
  base64 of the raw digest (`src/rgw/rgw_cksum.h:199-207@v20.2.4`).
- CRC digests are stored big-endian: each CRC is byte-swapped on a little-endian host before it
  is stored (`src/rgw/rgw_crc_digest.h:41-121@v20.2.4`).
- `user.rgw.cksum` holds `Cksum::encode`: ENCODE_START(2, 1), u16 type, u16 digest size, the
  digest bytes, u16 flags; a v1 record decodes with flags 0
  (`src/rgw/rgw_cksum.h:258-281@v20.2.4#rgw::cksum::Cksum::encode`).
- Flags: V2 0x1, COMPOSITE 0x2, FULL_OBJECT 0x4, COMBINED 0x8. `composite()` means both
  COMPOSITE and COMBINED are set (`src/rgw/rgw_cksum.h:104-113@v20.2.4`,
  `src/rgw/rgw_cksum.h:163-175@v20.2.4`).
- `get_checksum_type` reports FULL_OBJECT for a non-multipart object. For a multipart object it
  reports FULL_OBJECT only when V2 is set and the checksum is not composite. Every other
  multipart checksum is COMPOSITE (`src/rgw/rgw_cksum.h:382-399@v20.2.4`).
- The upload's algorithm and flags live in `multipart_upload_info`, ENCODE_START(4, 1), with a
  u16 type and u16 flags after the object-lock fields (`src/rgw/rgw_common.h:1552-1592@v20.2.4`).
  Squid's struct stops at version 2 with no checksum (`src/rgw/rgw_common.h:1508-1538@v19.2.6`).
- Each part's checksum is `RGWUploadPartInfo.cksum` in the part record, version 6 on Tentacle
  only. The part head carries it as `user.rgw.cksum` too
  (`src/rgw/driver/rados/rgw_putobj_processor.cc:525-584@v20.2.4#MultipartObjectProcessor::complete`).
  rgw-go already decodes both forms raw (§3).

**PutObject, UploadPart, UploadPartCopy** (`RGWPutObj::execute`):
1. The algorithm comes from `x-amz-checksum-algorithm`, else `x-amz-sdk-checksum-algorithm`
   (`src/rgw/rgw_cksum_pipe.h:65-82@v20.2.4`). Names match case-insensitively
   (`src/rgw/rgw_cksum.h:293-300@v20.2.4`).
2. Before the body is read, `RGWPutObj_Cksum::Factory` decides
   (`src/rgw/rgw_cksum_pipe.cc:40-73@v20.2.4#rgw::putobj::RGWPutObj_Cksum::Factory`):
   - an algorithm header whose value names no type, or has no value, throws EINVAL, which is
     400 InvalidArgument (`src/rgw/rgw_op.cc:4601-4611@v20.2.4`);
   - with no algorithm header, an upload's own type is used without a header (the part's
     override, `src/rgw/rgw_op.cc:4471-4472@v20.2.4`);
   - otherwise no checksum is computed, even when an `x-amz-checksum-<alg>` header or trailer
     is present.
3. The checksum filter is the outermost filter, so it hashes plaintext. Compression and
   encryption come after it (`src/rgw/rgw_op.cc:4563-4616@v20.2.4`).
4. After the body, the errors come in this order:
   - the length check;
   - aws4 completion;
   - the second quota check;
   - Content-MD5 (400 BadDigest);
   - the supplied ETag (422);
   - then the checksum (`src/rgw/rgw_op.cc:4757-4790@v20.2.4`).
5. The checksum step: the expected value is the request variable
   `HTTP_X_AMZ_CHECKSUM_<algorithm as sent>`. RGWEnv's map compares keys case-insensitively
   (`src/rgw/rgw_common.h:494@v20.2.4`). An aws-chunked trailer has put that variable into the
   environment by then (`src/rgw/rgw_auth_s3.cc:1459-1488@v20.2.4`).
   - With no expected value, the computed checksum is stored unverified.
   - A mismatch is 400 BadDigest.
   - The match compares base64 strings (`src/rgw/rgw_cksum_pipe.h:193-211@v20.2.4`).
6. The stored flags are the filter's flags: 0 for a PutObject, the upload's flags for a part.
   So `finalize()` clears V2 on every stored checksum (`src/rgw/rgw_cksum_pipe.h:187-191@v20.2.4`).
7. The response sends `x-amz-checksum-type: FULL_OBJECT` and the checksum header only for an
   AWS type, on PutObject and UploadPartCopy alike
   (`src/rgw/rgw_rest_s3.cc:2891-2925@v20.2.4#RGWPutObj_ObjStore_S3::send_response`).

**CreateMultipartUpload** (`src/rgw/rgw_rest_s3.cc:4507-4521@v20.2.4`):
- The algorithm comes from the same two headers. An unknown name is taken as none, without an
  error.
- `x-amz-checksum-type` sets the flags: `full_object` gives FULL_OBJECT, `composite` gives
  COMPOSITE, and any other value gives 0. With the header absent, the default is FULL_OBJECT
  for a CRC and COMPOSITE otherwise (`src/rgw/rgw_cksum_pipe.h:138-154@v20.2.4`).
- `permitted_cksum_algo_and_type` refuses with 400 InvalidRequest COMPOSITE with crc64nvme,
  and a FULL_OBJECT or 0 flag with a non-CRC type (`src/rgw/rgw_cksum.h:340-356@v20.2.4`).
- The type and flags go into `multipart_upload_info` (`src/rgw/rgw_op.cc:7003-7010@v20.2.4`,
  `src/rgw/driver/rados/rgw_sal_rados.cc:4150-4154@v20.2.4`).
- The response carries `x-amz-checksum-algorithm` upper-cased, and no `x-amz-checksum-type`
  (`src/rgw/rgw_rest_s3.cc:4540-4542@v20.2.4`).

**CompleteMultipartUpload** (`src/rgw/rgw_op.cc:7211-7341@v20.2.4`):
- A `get_info` failure on an upload with a checksum type is 500.
- After the completion lock, `try_sum_part_cksums` runs
  (`src/rgw/rgw_op.cc:7043-7161@v20.2.4#try_sum_part_cksums`):
  - It lists the upload's parts with `max = parts in the request` and loops on `truncated`.
    Each `list_parts` call clears its map (`src/rgw/driver/rados/rgw_sal_rados.cc:4193@v20.2.4`),
    so when the upload holds more parts than the request names, it combines the last page.
    It also combines every part in that page, not the parts the request lists (defect, §6).
  - A part without a checksum is 400 InvalidRequest, and so is a part of another type.
- The combiner (`src/rgw/rgw_cksum.cc:96-155@v20.2.4`):
  - CRC types always take the CRC combiner, because `CombinerFactory` ignores the upload's
    flags. It combines `crc(a||b)` from `crc(a)`, `crc(b)` and `len(b)` with madler's
    polynomial combination (`src/rgw/rgw_cksum.cc:31-89@v20.2.4#rgw::cksum::combine_crc_cksum`),
    then ORs FULL_OBJECT|COMBINED into the flags.
  - Other types digest the concatenated raw part digests and OR COMPOSITE|COMBINED into the
    flags.
- The armored value gets `-<parts in request>` when `composite()`.
- The supplied checksum is the first `x-amz-checksum-<alg>` header among crc32..blake3, so
  crc64nvme is never found (`src/rgw/rgw_cksum_pipe.h:120-136@v20.2.4`). It must equal the
  armored value, or match it up to the armored value's length less any `-N`, else 400 BadDigest.
- The combined checksum is emplaced into the head's attrs.
- The response carries `Checksum<ALG>` and `ChecksumType`
  (`src/rgw/rgw_rest_s3.cc:4635-4640@v20.2.4`).
- The request's per-part `Checksum<ALG>` elements are not parsed (`rgw_multi.cc` has none).
- A retried completion answers from the stored checksum (`check_previously_completed`,
  `src/rgw/rgw_op.cc:7474-7501@v20.2.4`).

**Reads.**
- GET and HEAD with `x-amz-checksum-mode` equal to `enabled` (case-insensitive) and no Range
  decode `user.rgw.cksum` (`src/rgw/rgw_rest_s3.cc:314-318@v20.2.4`,
  `src/rgw/rgw_rest_s3.cc:548-586@v20.2.4`):
  - COMPOSITE sends `<armor>-<parts count>`, otherwise the armored value;
  - `x-amz-checksum-type` is sent with it;
  - a record that does not decode sends neither header.
- A `partNumber` GET reads the part head's attrs (`src/rgw/driver/rados/rgw_rados.cc:7727-7783@v20.2.4`).
  Since a part's stored flags lack V2, the part's checksum is rendered as COMPOSITE with
  `-<parts>` (defect, §6). main renders it bare with the part's type
  (`src/rgw/rgw_rest_s3.cc:569-608@f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3`).
- GetObjectAttributes renders `Checksum` the same way, and each part's checksum from its part
  head (`src/rgw/rgw_rest_s3.cc:4029-4057@v20.2.4`, `src/rgw/rgw_rest_s3.cc:4083-4096@v20.2.4`,
  `src/rgw/driver/rados/rgw_sal_rados.cc:2908-2925@v20.2.4`). This carries the registry's
  "radosgw's ObjectParts gives a part without a checksum the checksum of the part before it".
- ListParts sends `ChecksumAlgorithm` only from the meta object's `user.rgw.cksum`, which
  nothing writes, so never (`src/rgw/rgw_rest_s3.cc:4695-4698@v20.2.4`). It sends each part's
  AWS-type checksum (`src/rgw/rgw_rest_s3.cc:4709-4713@v20.2.4`), and no `ChecksumType`.

**Copy and POST.**
- CopyObject neither computes nor checks a checksum. `set_copy_attrs` keeps the source's
  `user.rgw.cksum` under COPY and drops it under REPLACE
  (`src/rgw/driver/rados/rgw_rados.cc:3858-3885@v20.2.4#set_copy_attrs`).
- POST object maps an `x-amz-checksum-<alg>` form field to the algorithm and value
  (`src/rgw/rgw_rest_s3.cc:3104-3116@v20.2.4`), then verifies it as PutObject does
  (`src/rgw/rgw_op.cc:4994-5115@v20.2.4`).

**Types the suite expects that v20.2.4 lacks.** ceph main, where the s3-tests pin lives:
- renames type 3 `xxhash3`;
- makes xxhash3 and sha512 AWS types;
- adds md5 9, xxhash64 10 and xxhash128 11
  (`src/rgw/rgw_cksum.h:41-56@f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3`,
  `src/rgw/rgw_cksum.h:93-107@f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3`).

A v20.2.4 gateway indexes its nine-entry table with the stored type, so a type above 8 reads
past it (`src/rgw/rgw_cksum.h:88-99@v20.2.4`, `src/rgw/rgw_cksum.h:147-149@v20.2.4`).

### 2.2 Compression on the write path (both tags)

**Choosing a compressor.**
- The compression type is the destination placement's storage class's `compression_type`, or
  "none" (`src/rgw/driver/rados/rgw_zone.cc:356-365@v19.2.6#RGWZoneParams::get_compression_type`,
  `src/rgw/driver/rados/rgw_zone.cc:356-365@v20.2.4#RGWZoneParams::get_compression_type`,
  `src/rgw/rgw_zone_types.h:295-303@v20.2.4`).
- "random" picks a codec per object, and per upload by `std::hash` of the upload id
  (`src/rgw/rgw_op.cc:4088-4108@v19.2.6#get_compressor_plugin`,
  `src/rgw/rgw_op.cc:4297-4317@v20.2.4#get_compressor_plugin`).
- An unloadable plugin stores the object uncompressed.
- The compressor is skipped for an appendable write, and for an encrypted one unless the
  zonegroup has `compress-encrypted` (`src/rgw/rgw_op.cc:4341-4371@v19.2.6`,
  `src/rgw/rgw_op.cc:4563-4587@v20.2.4`).

**Blocks.**
- `RGWPutObj_Compress::process` compresses each `get_data` read as one block. A read is at
  most `rgw_max_chunk_size` of plaintext (`src/rgw/rgw_rest.cc:1073-1106@v20.2.4#RGWPutObj_ObjStore::get_data`).
- Each block records `{old_ofs = plaintext offset, new_ofs = end of the previous block, len}`.
- If the first block fails to compress, the whole object is stored uncompressed. A later
  block's failure is EIO.
- The next processor receives compressed bytes at compressed offsets
  (`src/rgw/rgw_compression.cc:44-88@v20.2.4#RGWPutObj_Compress::process`).
- So the head chunk and the tail stripes are cut from the compressed stream. The manifest's
  size is the compressed size, and the accounted size is the plaintext size.

**Attr.** When the compressor compressed, `user.rgw.compression` is written as
`RGWCompressionInfo{type, orig_size = plaintext size, compressor_message, blocks}`:
ENCODE_START(2, 1), with an `optional<int32>` message (`src/rgw/rgw_op.cc:4454-4463@v19.2.6`,
`src/rgw/rgw_op.cc:4686-4695@v20.2.4`, `src/rgw/rgw_compression_types.h:18-73@v20.2.4`).

**Codec framing**, which a writer must produce so that the other gateway decodes it:
- zlib: one byte 0, then a deflate stream at `compressor_zlib_level` (5) and
  `compressor_zlib_winsize` (-15: raw deflate). `compressor_message` is that winsize
  (`src/compressor/zlib/ZlibCompressor.cc:94-154@v20.2.4#ZlibCompressor::zlib_compress`; options
  `src/common/options/global.yaml.in:768-781@v20.2.4`).
- zstd: a le32 decompressed length, then one frame at `compressor_zstd_level` (1)
  (`src/compressor/zstd/ZstdCompressor.h:29-60@v20.2.4`).
- lz4: a u32 count, then a (u32 origin_len, u32 compressed_len) pair per input buffer, then
  the data. The input is made contiguous first, so the count is 1
  (`src/compressor/lz4/LZ4Compressor.cc:36-80@v20.2.4#LZ4Compressor::compress`).
- snappy: a raw snappy block.

The read path (phase 1) already decodes all four.

**Hints.** Every tail write carries `set_alloc_hint2(0, 0, flags)`, with flags INCOMPRESSIBLE
(512) when the object is compressed (`src/rgw/driver/rados/rgw_putobj_processor.cc:146-155@v20.2.4#RadosWriter::add_write_hint`).
The head's `write_full` carries INCOMPRESSIBLE when compressed
(`src/rgw/driver/rados/rgw_rados.cc:3326-3335@v20.2.4`, `src/include/rados/librados.h:157@v20.2.4`).

**Multipart.**
- Each part compresses on its own, and its part record carries its `cs_info`
  (`src/rgw/driver/rados/rgw_putobj_processor.cc:586-591@v20.2.4`).
- Completion refuses `ERR_INVALID_PART` when a part differs from the first in being
  compressed, in type, or in message. It then concatenates the block maps, shifted by the
  plaintext and compressed offsets so far (`src/rgw/driver/rados/rgw_sal_rados.cc:4379-4414@v20.2.4`).
  Phase 1 serves this already (§3).

**Copy.**
- Squid's `copy_obj_data` copies stored bytes and keeps the source's compression attr. It never
  consults the destination's compression type (the exclusion "A copy that streams its data
  copies the stored bytes"; owner decision 4).
- Tentacle's `RGWCopyObjDPF` decodes a compressed source and re-encodes it with the destination
  placement's type. It also compresses an uncompressed source (`src/rgw/rgw_op.cc:5825-5884@v20.2.4`).
- UploadPartCopy reads its source decompressed on both tags and recompresses through the part
  writer (`src/rgw/rgw_op.cc:4227-4266@v20.2.4`).
- Lifecycle's `transition_obj` copies stored bytes with no data-processor factory on both tags
  (`src/rgw/driver/rados/rgw_rados.cc:5417-5487@v20.2.4#RGWRados::transition_obj`).

### 2.3 Placement on the write path (both tags)

**The destination placement.**
- `rgw_build_bucket_policies` builds `s->dest_placement` from `x-amz-storage-class` inherited
  over the bucket's rule. An invalid placement is 400 InvalidArgument
  (`src/rgw/rgw_op.cc:576-583@v19.2.6`).
- `AtomicObjectProcessor::prepare` puts the head in the bucket rule's data pool and the tails
  in the destination class's pool. Its head chunk is 0 when the tail pool differs, or when
  the placement keeps no inline data
  (`src/rgw/driver/rados/rgw_putobj_processor.cc:268-341@v19.2.6#AtomicObjectProcessor::prepare`).
- Phase 1 reproduces all of this (§3). What phase 2 adds from the placement is its
  compression type (§2.2).

**Lifecycle's transition** resolves a target class within the bucket's placement and checks
`valid_placement`. It then streams stored bytes with no filters (lock-lifecycle's research, its
§2.4).

### 2.4 `?layout` (both tags)

**Dispatch.** `?layout` is a branch of `RGWHandler_REST_Obj_S3::op_get`, after `acl` and
`uploadId` and before `tagging` (`src/rgw/rgw_rest_s3.cc:4805-4813@v19.2.6`,
`src/rgw/rgw_rest_s3.cc:5360-5367@v20.2.4`). HEAD has no such branch.

**Permission.**
- `RGWGetObjLayout` checks only the `admin=read` cap, and evaluates no ACL or policy. Its op
  mask is READ and its name `get_obj_layout`; `execute` is empty
  (`src/rgw/rgw_op.h:2537-2554@v20.2.4#RGWGetObjLayout`, `src/rgw/rgw_op.cc:7891-7898@v19.2.6`,
  `src/rgw/rgw_op.cc:8809-8816@v20.2.4`).
- The object's policies are read first, as for every object op. So a missing object answers
  404 to a requester who may list the bucket, and on Tentacle with `x-amz-delete-marker: false`
  (the exclusion "Tentacle's x-amz-delete-marker is false, or absent, on a missing object").

**Body.**
- A JSONFormatter `{"result": {...}}` is sent after `end_header(..., "application/json")` with
  no proposed length. The buffering filter then sends a Content-Length of the body
  (`src/rgw/rgw_client_io_filters.h:208-236@v20.2.4`).
- `dump_obj_layout` prepares a read, which follows the OLH (versioning's area), and fetches the manifest.
  Its return value is ignored (`src/rgw/rgw_rest_s3.cc:4338-4355@v19.2.6`,
  `src/rgw/rgw_rest_s3.cc:4879-4896@v20.2.4`).
- The result holds:
  - `head`, an `rgw_raw_obj` of `{pool, oid, loc}`
    (`src/rgw/rgw_common.cc:3131-3136@v19.2.6`, `src/rgw/rgw_common.cc:3193-3198@v20.2.4`);
  - `manifest`, `RGWObjManifest::dump`;
  - `data_location`, an array of one `{ofs, loc, loc_ofs, loc_size}` object per manifest
    stripe, where `loc_size` is the stripe size capped at what is left of the object
    (`src/rgw/driver/rados/rgw_sal_rados.cc:2779-2825@v19.2.6#RadosObject::dump_obj_layout`,
    `src/rgw/driver/rados/rgw_sal_rados.cc:3615-3661@v20.2.4#RadosObject::dump_obj_layout`).
- **Differs:** the manifest dump prints `tier_config` for `cloud-s3` only at v19.2.6, and for
  every supported tier type at v20.2.4 (`git diff v19.2.6 v20.2.4 -- src/rgw/driver/rados/rgw_obj_manifest.cc`).

### 2.5 The admin otp metadata section (both tags; cls_otp identical)

**Objects and key.**
- An OTP object is `user:<rgw_user string>` in the zone's `otp_pool`
  (`src/rgw/services/svc_cls.h:56-58@v19.2.6`, `src/rgw/services/svc_cls.h:56-58@v20.2.4`).
- The section's key is that object name, with no prefix: Squid maps key to oid one to one
  (`src/rgw/services/svc_otp.cc:15-47@v19.2.6`). The metadata key is `otp:user:<uid>`
  (`src/rgw/services/svc_meta_be_otp.cc:20-23@v19.2.6`,
  `src/rgw/driver/rados/rgw_otp.cc:145-148@v20.2.4`).

**get** is one read op with stat2 (for the mtime), a cls_version read, and `otp.otp_get` with
`get_all` (`src/rgw/services/svc_cls.cc:218-245@v19.2.6`). It answers `{"devices": [...]}`,
each `otp_info_t` dumped as type (int), id, seed, `seed_type` (`hex`, `base32` or `unknown`),
`time_ofs`, `step_size` and `window`. `seed_bin` is never shown
(`src/rgw/driver/rados/rgw_otp.cc:47-49@v19.2.6`, `src/rgw/driver/rados/rgw_otp.cc:24-26@v20.2.4`,
`src/cls/otp/cls_otp_types.cc:30-71@v19.2.6`).

**put** reads nothing first and applies every put, whatever `update-type` says
(`src/rgw/driver/rados/rgw_otp.cc:120-140@v19.2.6`, `src/rgw/driver/rados/rgw_otp.cc:70-82@v20.2.4`).
It is one write op (`src/rgw/services/svc_cls.cc:65-87@v19.2.6`,
`src/rgw/services/svc_cls.cc:188-216@v19.2.6`):
- `remove` with FAILOK, then `create(false)`;
- the version set: the document's `ver` when its tag is non-empty, else a fresh random tag at
  version 1;
- `mtime2`, the document's mtime;
- `otp.otp_set` with the device list. That call reads each id's instance, replaces its
  `otp_info_t`, parses the seed (hex, or base32, else EINVAL) and records the id in the
  `header` omap key (`src/cls/otp/cls_otp.cc:268-348@v19.2.6`). The object's removal first
  resets every device's replay state.

**put's status differs between the tags.** Squid answers 204 with `RGWX_UPDATE_STATUS:
applied` (`STATUS_APPLIED`). Tentacle answers 200 without that header, as its handler returns
the mdlog's 0. Both send `RGWX_UPDATE_VERSION: ver:0,tag:`, because no read version is ever
set (`src/rgw/rgw_rest_metadata.cc:234-284@v20.2.4`).

**remove** reads, then removes the object under the version read
(`src/rgw/rgw_metadata.cc:554-573@v19.2.6`, `src/rgw/driver/rados/rgw_otp.cc:84-92@v20.2.4`).

**list** lists every object of the otp pool (`src/rgw/driver/rados/rgw_otp.cc:110-138@v20.2.4`).

**cls_otp's wire types**, all v1:
- `otp_info_t`: u8 type (TOTP 2), id, seed, u8 seed type, bufferlist `seed_bin`, i32
  `time_ofs`, u32 `step_size` (30), u32 `window` (2)
  (`src/cls/otp/cls_otp_types.h:31-77@v19.2.6`);
- `cls_otp_set_otp_op{list<otp_info_t>}`, `cls_otp_get_otp_op{bool get_all, list<id>}` and
  `cls_otp_get_otp_reply{list<otp_info_t>}`, with the remove, check and result ops beside them
  (`src/cls/otp/cls_otp_ops.h:11-167@v19.2.6`).

The methods are `otp_set` (RD|WR), `otp_get` (RD), `otp_remove` (RD|WR), `otp_check` (RD|WR),
`otp_get_result` (RD) and `get_current_time` (RD) (`src/cls/otp/cls_otp.cc:558-575@v19.2.6`).
`git diff v19.2.6 v20.2.4 -- src/cls/otp` changes only the client's `otp_check`, from
`ioctx.exec` to a write operation.

**No other admin op touches OTP.** No other `/admin` op reads or writes it; only S3's
`x-amz-mfa` does (grep of `rgw_rest*.cc`).

### 2.6 Interaction with phase 1

- **Quota and stats.** A compressed object's accounted size, which quota and the index entry
  use, is the plaintext size. Phase 1's `putStream` already takes it from the compression attr
  (`internal/driver/put.go:70-146`).
- **Multipart.** Checksums add the upload's type to `multipart_upload_info` and the part's
  checksum to `RGWUploadPartInfo`, both Tentacle struct fields. Compression adds the part's
  `cs_info`, a field on both tags. Completion keeps its named lock, and the combined checksum
  is computed under it.
- **ACL, policy and the reshard protocol.** None changes: the new state is head attrs written
  in the same guarded head write.
- **Cache invalidation.** The otp section is not cached by radosgw's metadata cache: OTP is
  read only through cls calls. A put sends no cache notify, as radosgw sends none for otp.
- **The copy path.** Owner decision 4 keeps raw copies for compressed sources. Checksums ride
  in the attrs `copyAttrs` carries.

## 3. What rgw-go already has

All paths at origin/main 0239625.

**Checksums.**
- Read-side attr names and decoding:
  - `meta.AttrCksum` names `user.rgw.cksum` (`internal/meta/attrs_read.go:28`).
  - `meta.UploadPartInfo.Cksum` carries a part record's checksum as raw encoded bytes
    (`internal/meta/multipart.go:114-117`), and it decodes and re-encodes at version 6 on
    Tentacle (`internal/meta/multipart.go:120-190`).
  - `meta.MultipartUploadInfo` has `CksumType` and `CksumFlags`, encoded at version 4 on
    Tentacle and version 2 on Squid (`internal/meta/multipart.go:201-260`).
  - There is no typed `Cksum` and no package that computes one.
- The write path stores nothing:
  - `CreateUpload` writes the upload info with no checksum type (`internal/driver/mp_upload.go:15-50`).
  - `PutParams` has no checksum field (`internal/op/object.go:50-79`).
  - Tentacle's request-attr blocklist keeps `x-amz-checksum-algorithm` out of the attrs
    (`internal/s3/requestattrs.go:25-40`).
- Reads show none: GetObjectAttributes renders an empty `<Checksum></Checksum>`
  (`internal/s3/getobjattrs.go:75-87`), and `op.ObjectPart` has no checksum
  (`internal/op/objectread.go:264-269`).
- Trailers:
  - The aws-chunked reader parses and signs the trailer section only for the signed-trailer
    payload, and it discards the values (`internal/auth/chunked.go:382-433`).
  - The unsigned-trailer payload's section is not parsed at all
    (`internal/auth/chunked.go:395-398`).
  - `op.AuthResult` has no trailer accessor (`internal/op/identity.go:58-81`).
- Exclusions this area retires or rewrites:
  - "Client checksums are ignored until phase 2" (`docs/exclusions.md:2152-2191`);
  - "Stored checksums are not shown" (`docs/exclusions.md:4013-4022`);
  - the multipart line of "Multipart uploads in flight are shared" (`docs/exclusions.md:686-687`);
  - "aws-chunked framing is read strictly"'s checksum sub-bullet (`docs/exclusions.md:1810-1816`).

**Compression.**
- `internal/compression` decodes only (`Decoder` and `NewDecoder`,
  `internal/compression/codec.go:51-80`). `meta.CompressionInfo.Encode` already exists
  (`internal/meta/compression.go:38`).
- `putStream` hands the body straight to the tail writer, and hashes the same bytes for the
  ETag and Content-MD5 (`internal/driver/put.go:70-146`). It already takes the accounted size
  from a compression attr in `attrs`, the stored-byte copy's case.
- The part record gets `Compression: meta.NewCompressionInfo()`, type none
  (`internal/driver/mp_part.go:183`), and completion already merges and refuses mixed parts
  (`internal/driver/mp_complete.go:39-60`; the attr at `internal/driver/mp_complete.go:360`).
- Every tail write carries `SetAllocHint(0, 0, 0)` (`internal/driver/stripe.go:308`,
  `internal/driver/stripe.go:540`), and the head write carries no hint.
- The placement's compression type is already resolved: `op.Placement.Compression`
  (`internal/op/zone.go:18-19`), filled from the class (`internal/driver/zone.go:461-489`).
- Copy keeps a compressed source's attr (`internal/driver/copy.go:174-175`). A streamed copy is
  `copyData` (`internal/driver/copy.go:431`), and UploadPartCopy streams the decoded source
  through `PutPart` (`internal/driver/mp_part.go:291-296`).
- No exclusion records that a PUT into a compressing placement is stored uncompressed. Only
  the copy entry mentions it (`docs/exclusions.md:2207-2225`). The ranged-GET entry already
  states the block-size obligation (`docs/exclusions.md:3941-3949`).

**Placement.** Phase 1 serves placement on the write path:
- `planPut` resolves the head rule's pool and the class's tail pool. It sets the head chunk to
  0 when they differ, and honours `inline_data` (`internal/driver/stripe.go:66-127`).
- The class label is written on the head (`internal/driver/headwrite.go:262`).
- `checkDestPlacement` refuses a class the placement lacks (`internal/op/putobject.go:190`).
- CreateBucket takes the location constraint's placement and `x-amz-storage-class`
  (`internal/s3/bucket.go:83-113`; `selectBucketPlacement`, `internal/op/bucketops.go:335`).
- Copy across classes streams (`internal/driver/copy.go:431`), and a copy is labelled with its
  destination's class (`internal/driver/copy.go:154`).
- The integration spec "writes to a storage class's placement" covers it
  (`test/integration/write_test.go:193`).
- `op.ZoneInfo.Placement(rule)` is `valid_placement` plus the pools and compression type
  (`internal/op/zone.go:24-42`): the seam lifecycle's transition needs. It exists, so this area
  adds none.
- One placement-adjacent Tentacle feature stays 501: CreateBucketConfiguration's `BucketIndex`
  (`internal/op/bucketops.go:89-91`, `internal/op/bucketops.go:246-248`). §9 does not list it
  (open question 4).

**`?layout`.**
- The dispatch row exists (`internal/s3/dispatch.go:207`). No handler is bound, so the handler
  answers 501 (`internal/s3/handler.go:379`; `docs/exclusions.md:4127-4132`).
- `meta.Manifest.MarshalJSON` renders v20.2.4's `RGWObjManifest::dump`, begin and end iterators
  included (`internal/meta/manifest.go:53-100`). `meta.Pool` marshals as a string
  (`internal/meta/pool.go:78`).
- The admin cap check exists (`op.CheckCaps`, used at `internal/op/adminmetadata.go:205-207`).

**Admin otp.**
- The metadata op serves `user`, `bucket` and `bucket.instance` (`internal/op/adminmetadata.go:28-31`).
  The driver's `Get`, `Put`, `Remove` and `sectionListing` switch on those three
  (`internal/driver/metadata.go:24-26`, `internal/driver/metadata.go:92-104`,
  `internal/driver/metadata.go:162-175`, `internal/driver/metadata.go:651-663`,
  `internal/driver/metadata.go:738-766`).
- `PutMetadata` applies `check_versions` to every section and names `UpdateStatus` per section
  (`internal/op/adminmetadata.go:202-262`).
- `meta.ZoneParams.OTPPool` exists, defaulting to `<zone>.rgw.otp`
  (`internal/meta/zone_params.go:32`, `internal/meta/zone_params.go:157`).
- There is no `internal/cls/otp` and no fakerados otp class.
- The exclusion to rewrite is "The admin metadata API serves three sections"
  (`docs/exclusions.md:1646-1655`).

**What must change.** Each item is §7's task list:
- a typed checksum package;
- trailer values exposed by auth;
- checksum params and results on the op and store seams;
- the driver's body path split into plaintext hashing and a compressing writer;
- encoders in `internal/compression`;
- an INCOMPRESSIBLE hint;
- the part record's checksum and compression;
- checksum combination at completion;
- read-side rendering;
- the `?layout` handler;
- the otp section behind a section table;
- the s3-tests include list.

## 4. Coexistence

What both gateways must read and write alike, beyond phase 1's obligations.

**Checksums.**
- `user.rgw.cksum` is `Cksum::encode` exactly: struct v2, compat 1, the u16 type, the u16
  digest size equal to the type's, the digest (CRCs big-endian), and the u16 flags.
- rgw-go writes radosgw's bytes where radosgw is right:
  - flags 0 on a PutObject's checksum and the upload's flags on a part's, as `finalize()`
    leaves them;
  - `V2|COMBINED|FULL_OBJECT` on a combined CRC;
  - `V2|COMBINED|COMPOSITE` on a composite digest.
- rgw-go differs only where radosgw is wrong (§6): a single-part CRC upload, a CRC upload the
  client asked to be COMPOSITE, and an upload holding more parts than the completion names.
  Each such record is still one a Tentacle radosgw decodes and renders consistently.
- It never writes a type above 8: a v20.2.4 gateway indexes its table past its end on any read
  of one.
- On read it decodes every version, 1 and 2, and every type 0-8. A type, digest size or record
  it cannot decode shows no checksum, as radosgw does on a decode failure. It never indexes
  past its own table.
- On Tentacle, `multipart_upload_info` v4 carries the upload's type and flags and
  `RGWUploadPartInfo` v6 the part's checksum. On Squid both stay at Squid's versions, without
  them. A part head carries the part's `user.rgw.cksum` on both releases.
- An upload either gateway begins can be extended and completed by the other. The types must
  agree, or completion refuses with 400 InvalidRequest, as radosgw does.
- Squid's radosgw ignores `user.rgw.cksum`, and its COPY directive carries the attr to the copy
  (`set_copy_attrs` is the same at both tags; §2.1). Data a Squid radosgw copies is unchanged,
  so the carried checksum stays true, except for a multipart source streamed to another
  placement: its composite value then sits on an object with no parts. rgw-go shows no checksum
  for that case (§7 WP-12).

**Compression.**
- `user.rgw.compression` is `RGWCompressionInfo` v2 with the type name, plaintext size,
  message and block map.
- Each block is one compressor call on at most `rgw_max_chunk_size` plaintext bytes, never
  more. A block larger than a coexisting radosgw's `rgw_max_chunk_size` exposes that radosgw
  to its ranged-GET loop (`docs/exclusions.md`, "A ranged GET of a compressed object does not
  spin").
- Each block's bytes are the codec's Ceph framing (§2.2), so the other gateway decodes them.
- zlib's `compressor_message` must state the window and wrapper rgw-go actually used. Go's
  deflate always uses a 32 KiB window, so rgw-go writes raw deflate with message -15, or the
  zlib or gzip wrapper with 15 or 31 when `compressor_zlib_winsize` asks for one. It never
  claims a narrower window: a stream with 32 KiB back-references does not inflate under a
  smaller one.
- The INCOMPRESSIBLE hint (512) on compressed tails and heads is a performance obligation
  only.
- All parts of one upload share type and message, or either gateway's completion refuses them.
  With `compressor_zlib_winsize` narrower than 15 bits, radosgw's parts and rgw-go's differ in
  message, so a mixed upload cannot complete (an exclusion; the default -15 is unaffected).

**Copy.** Owner decision 4 stands for a compressed source: a raw copy keeps its codec. A
streamed copy of an uncompressed source into a compressing placement is compressed, on both
releases (§7 WP-11). Either gateway reads both forms.

**`?layout`.** No shared state: it reads what is stored. rgw-go's output is a second oracle
for its manifest and pool resolution.

**otp.**
- The object is `user:<uid>` in the otp pool, written only through cls_otp `otp_set` after
  remove and `create(false)`, with the document's version and mtime.
- Reads go through `otp_get`. rgw-go never touches the omap directly, so the class's storage
  layout stays the class's business.
- A device an admin writes through either gateway is checked by either gateway's MFA path
  (versioning's).

**Cross-gateway gate cases.** These are new rows in the shared-zone gate; "A→B" means A
writes and B reads, and every row runs both ways unless marked.
1. **Compression.**
   - Through each codec's class (`populate.sh` already makes one per codec on the STANDARD
     pool), a PUT of 10 MiB is read by the other gateway whole, by ranges inside and across
     blocks, and through `radosgw-admin object stat`. The stored block map is radosgw's
     shape: one block per chunk, back to back.
   - rgw-go→radosgw is new; radosgw→rgw-go is phase 1's read gate.
2. **Compressed multipart.** One upload into a compressing class with parts from both
   gateways is completed by each, then read by both.
3. **Checksums (Tentacle).**
   - PutObject with each served algorithm, in header and trailer form, then HEAD with
     checksum mode and GetObjectAttributes through the other.
   - A COMPOSITE SHA256 upload and a FULL_OBJECT CRC32 upload, each begun on one gateway with
     parts through both, completed on the other.
   - A retried completion answered by the gateway that did not complete it.
   - `radosgw-admin object stat` shows the same `user.rgw.cksum` bytes for the same input
     either way. The cases of §6 #2-#4 are excluded from that comparison and pinned
     separately.
4. **Checksums (Squid).** rgw-go's checksummed PUT is read whole by Squid's radosgw, and a
   Squid radosgw COPY keeps the attr.
5. **Copy.** rgw-go streams a radosgw-compressed source, and Tentacle's radosgw copies an
   rgw-go-compressed object. Both read back byte-exact through both.
6. **`?layout`.** For every populated object and for rgw-go-written ones, rgw-go's and
   radosgw's `?layout` bodies are byte-identical (one way: the same object read by both).
7. **otp.**
   - `radosgw-admin mfa create` is then read through rgw-go's `GET /admin/metadata/otp`.
   - rgw-go's metadata put is then listed by `radosgw-admin mfa list`, and accepted by
     `radosgw-admin mfa check` with a TOTP the spec computes.
   - Metadata list and remove go through each gateway.

## 5. Tests

### 5.1 s3-tests

At the pin f6f2529dd9ef the suite has no test of compression, `?layout`, otp devices or MFA
(grep of `test_s3.py` and `test_headers.py`). This area's s3-tests are all checksum tests.
Storage-class copies are already phase 1's, and the only s3-tests that use storage classes
are held out by markers.

| Source | Tests | Today | Phase 2 |
|---|---|---|---|
| `deselect-phase2.txt:131` | test_get_checksum_object_attributes | deselected | delete the line |
| `checksum` marker | test_object_checksum_{sha256,sha512,xxhash3,xxhash64,xxhash128,md5,crc64nvme}, test_multipart_checksum_sha256, test_multipart_reupload_checksum_and_etag, test_multipart_use_cksum_helper_{sha256,crc64nvme,crc32,crc32c,sha1}, test_get_multipart_checksum_object_attributes, test_post_object_upload_checksum (16 tests) | `EXCLUDED_MARKERS` | drop `checksum` from the markers. The 17th, test_multipart_sse_c_checksum_complete, stays out under `encryption` (phase 4). test_post_object_upload_checksum needs POST object: if bucketconfig's BC-34 has not landed, add it to `deselect-phase2.txt`'s POST section, and BC-34 deletes it. |
| `encryption` marker, `unencrypted-unencrypted-*` parametrizations | test_copy_enc[unencrypted-unencrypted-<sc>-<sc>-<size>] (4 sizes × classes²) and test_copy_part_enc[unencrypted-unencrypted-<sc>-<sc>-8388608] | `EXCLUDED_MARKERS` | optional: copies across storage classes, which only an include list can run. Their ids depend on the conf's `storage_classes` (lifecycle's C12 adds two classes), so the list is written after C12. |

Predicted outcomes, from the source, none recorded. The recording task confirms them on both
releases before any comparison.
- **Squid's radosgw fails every checksum test** (no checksum code). rgw-go serves single-object
  checksums on Squid (open question 2), so the single-object tests it passes become
  `known-differences-squid.txt` lines under one exclusion: test_object_checksum_sha256,
  _crc64nvme and, per open question 1, _sha512 and _xxhash3, plus
  test_get_checksum_object_attributes. The multipart checksum tests fail on both.
- **Tentacle's radosgw:**
  - passes: test_object_checksum_sha256 and _crc64nvme, test_multipart_checksum_sha256,
    test_multipart_reupload_checksum_and_etag, test_get_checksum_object_attributes,
    test_get_multipart_checksum_object_attributes;
  - fails test_object_checksum_sha512: it answers `x-rgw-checksum-sha512`, where boto reads
    `x-amz-checksum-sha512`;
  - fails _xxhash3, _xxhash64, _xxhash128 and _md5: v20.2.4 knows none of those names, so 400
    InvalidArgument;
  - fails all five test_multipart_use_cksum_helper_*: the `partNumber` GET renders the part's
    checksum with `-3` and COMPOSITE (§6 #5).
- **rgw-go**, following AWS and main where the stored form allows:
  - passes the five helpers, and sha512 and xxhash3 if open question 1 takes them. Each is a
    `known-differences-tentacle.txt` line;
  - fails md5, xxhash64 and xxhash128 as radosgw does, refusing types a Tentacle radosgw
    cannot decode.
- **Every PUT in the set may carry a CRC32.** The suite's boto3 is unpinned
  (`requirements.txt`) and its clients set only the signature version, so the venv's botocore
  defaults decide whether ordinary PUTs carry a CRC32 header or trailer, and whether GETs ask
  for checksum mode. If they do, phase 2's checksum code runs on nearly every test of the set
  on both releases, not only the 16. The recording task reads botocore's version from the
  run's `.freeze` and says which.

**Harness deltas** (hack/s3tests; this area plans them as WP-22 and WP-23, §7.7):
- **The include list.** Adopt bucketconfig's `hack/s3tests/include-phase2.txt` (its open
  question 7) with three refinements:
  1. **Selection moves to Go first.** `run.sh` is 363 lines whose deselect validation is
     already program logic. The owner's standing rule is never to grow a large script for a
     feature, so the harness task ports the selection to Go before the include list is added:
     marker expression, deselect lists, include list, prefix checks and the stale-line check,
     in hack/parity or a small `hack/s3tests` command. `run.sh` then only sequences the
     cluster, the venv and pytest.
  2. **One pytest run.** Collect the node ids twice, with the marker expression and without
     it. The set is (marked-in ∪ include) − deselect, validated as `run.sh` validates today:
     an include line must name a test the markers exclude, or it is stale. Pytest then runs
     the explicit node ids without `-m`, so one junit report remains.
  3. **The selection's hash covers all three lists.** `--print-deselect` hashes the deselect
     lists into each ledger entry's `meta.deselect` (for example
     `test/s3tests/baseline/tentacle.json`); it must hash the include list too, or a recorded
     outcome would not say which selection produced it.
- **Markers.** This area removes `checksum` from `EXCLUDED_MARKERS` and deletes
  `deselect-phase2.txt:130-131`. Each area edits only its own lines (lock-lifecycle's hot-file
  rule).

### 5.2 go-ceph rgw/admin suite

None. `hack/admin/deselect-phase2.txt` lists only versioning and object lock. go-ceph's
rgw/admin has no metadata or MFA call, so the otp section has no admin-suite case. Rook's
operator calls neither.

### 5.3 Specs the plan must require

**fakerados.**
- An otp class, method table first (§7 WP-17): `otp_set`, `otp_get` and `otp_remove`,
  `get_current_time`, and the omap layout `header` and `otp/<id>` kept internal. Specs per
  method: EINVAL on a bad request, a bad seed and a missing object, and `get_all` against
  `ids`. `otp_check` and `otp_get_result` are versioning's, from the same table.
- No other emulator work: checksums and compression are xattrs and data that the existing
  steps carry. The alloc hint is recorded already.

**Unit specs.**
- **Checksum package:**
  - each algorithm against radosgw's unit-test vectors: CRC32 `mLLFvQ==` and CRC32C `ldwuSw==`
    (`src/test/rgw/test_rgw_cksum.cc:106-143@v20.2.4`), and the awscli CRC64NVME values
    `wiBA+PSv41M=` and `oa2U66pdPLk=` (`src/test/rgw/test_rgw_cksum.cc:440-467@v20.2.4`);
  - the s3-tests vectors: 1024 × `A` gives SHA256 `arcu6553sHVAiX4MjW0j7I7vD4w6R+Gz9Ok0Q9lTa+0=`
    and CRC64NVME `Qeh8oXvGiSo=`; the 5 MiB A/B/C parts and their combined values for CRC32
    `WgDhBQ==`, CRC32C `xU+Krw==`, CRC64NVME `i+6LR0y3eFo=`, SHA1
    `sizjvY4eud3MrcHdZM3cQ/ol39o=-3` and SHA256 `uWBwpe1dxI4Vw8Gf0X9ynOdw/SS6VBzfWm9giiv1sf4=-3`;
  - the property crc(a‖b) = combine(crc(a), crc(b), len(b)) over random splits;
  - encode and decode of v1 and v2 records, generated vectors captured from radosgw-written
    attrs (ceph-dencoder has no `rgw::cksum::Cksum`), a refused type above 8, and a digest
    size other than the type's;
  - the type and flags tables of §2.1 as `DescribeTable` rows.
- **Compression encoders:** for each codec, a round trip through phase 1's own decoders; the
  framing bytes (zlib's leading 0 and message, zstd's le32 length, lz4's count 1 and pair);
  blocks never longer than the chunk size; a first-block failure stored raw.
- **Driver write path on fakerados:**
  - a PUT into a compressing class: head and tails cut from the compressed stream; the
    manifest's size compressed and the index entry's accounted size plaintext; the hint 512 on
    every tail write and on the head write; the attr's block map;
  - checksum stored only when verified or unverifiable; BadDigest after Content-MD5; nothing
    written on a mismatch;
  - parts' records carrying `cs_info` and, on Tentacle, the checksum;
  - completion's combination over exactly the listed parts; the supplied-value comparison
    before the head write.
- **Op and handler specs with counterfeiter fakes:**
  - the algorithm precedence and every refusal with its error and order;
  - response headers on PUT, UploadPartCopy, CreateMultipartUpload and CompleteMultipartUpload;
  - GET and HEAD rendering (Range, `partNumber`, composite, decode failure);
  - GetObjectAttributes, ListParts;
  - `?layout`'s cap check, missing object and body;
  - the otp section's get, put, remove and list.
- **auth:** trailer values for the signed and the unsigned trailer payloads, available at EOF
  and not before; a name `x-amz-trailer` lists that never arrives.

**Integration specs** (cluster): §4's seven gate cases.

## 6. Floor defects

Classes are the owner's: security, data corruption, S3-correctness, cosmetic. "Unreproduced"
means the entry is derived from the source. Each new entry goes into
`docs/ceph-upstream-bugs.md` with that status, and the task named last in its row budgets the
cluster reproduction (PP-11) and runs the prior-art search before filing. None has been
searched yet. All are v20.2.4 unless stated, since Squid has no checksum code.

| # | Defect | Class | Status | rgw-go's choice (task) |
|---|---|---|---|---|
| 1 | CompleteMultipartUpload combines the wrong parts. `try_sum_part_cksums` pages `list_parts` with a page size equal to the number of parts in the request, and keeps only the last page: each call clears the map (`src/rgw/rgw_op.cc:7068-7092@v20.2.4`, `src/rgw/driver/rados/rgw_sal_rados.cc:4193@v20.2.4`). It then combines every part of that page. So an upload holding parts the request leaves out gets a stored checksum of other parts, and a FULL_OBJECT CRC that SDKs validate on GET then fails the download. | S3-correctness (integrity metadata wrong; data intact) | unreproduced | Combine exactly the parts the request names, in order, from the assembly the completion already does. New exclusion (WP-9). Reproduced in WP-14. |
| 2 | `CombinerFactory` ignores the upload's flags (`src/rgw/rgw_cksum.cc:145-155@v20.2.4`). A CRC32 or CRC32C upload created with `x-amz-checksum-type: COMPOSITE`, which `permitted_cksum_algo_and_type` accepts, gets a combined full-object CRC stored as FULL_OBJECT, not the composite it asked for. | S3-correctness | unreproduced; also on main at the pin (`src/rgw/rgw_cksum.cc:147-157@f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3`) | Compute the composite: the CRC of the concatenated big-endian part CRCs, flags `V2\|COMBINED\|COMPOSITE`, which a Tentacle radosgw renders with `-N`. New exclusion (WP-2, WP-9). |
| 3 | A single-part CRC upload is reported COMPOSITE. Its combined record keeps the part's flags (FULL_OBJECT, without V2, since `finalize()` clears V2: `src/rgw/rgw_cksum_pipe.h:187-191@v20.2.4`, `src/rgw/rgw_cksum.cc:129-142@v20.2.4`). `get_checksum_type` then says COMPOSITE, so GET and GetObjectAttributes send `<crc>-1` as COMPOSITE, and the completion answers the bare CRC with ChecksumType COMPOSITE. | S3-correctness | unreproduced | Store `V2\|COMBINED\|FULL_OBJECT` for every combined CRC. New exclusion (WP-2). |
| 4 | Two crc64nvme checks are skipped. A supplied `x-amz-checksum-crc64nvme` on CompleteMultipartUpload is never compared: `find_hdr_cksum` scans crc32..blake3 (`src/rgw/rgw_cksum_pipe.h:120-136@v20.2.4`). A part's `x-amz-checksum-crc64nvme` sent without an algorithm header is never compared either: `cksum_algorithm_hdr(Type)` has no crc64nvme case (`src/rgw/rgw_cksum_pipe.h:33-63@v20.2.4`). So a wrong value is accepted and the computed one stored. | S3-correctness (integrity check skipped) | unreproduced; main adds no case (to verify in WP-14) | Compare for every type. New exclusion (WP-6, WP-9). |
| 5 | A `partNumber` GET with checksum mode renders the part's checksum as `<armor>-<parts>` with COMPOSITE. It reads the part head's record, whose flags lack V2, through the whole-object rule (`src/rgw/rgw_rest_s3.cc:548-586@v20.2.4`). | S3-correctness | unreproduced. s3-tests' five `test_multipart_use_cksum_helper_*` expect the fix, which main carries (`src/rgw/rgw_rest_s3.cc:569-608@f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3`) | Follow main: the part's bare value, with COMPOSITE only when its flags say so. New exclusion and `known-differences-tentacle.txt` lines (WP-12, WP-13, WP-14). |
| 6 | A checksum value without an algorithm header is not verified. A PutObject or UploadPart carrying `x-amz-checksum-<alg>`, or a trailer naming one, without `x-amz-(sdk-)checksum-algorithm` is stored without any checksum and never verified (`src/rgw/rgw_cksum_pipe.cc:46-72@v20.2.4`). AWS verifies such a value. | S3-correctness (integrity check skipped) | unreproduced | Take the algorithm from the lone value header or from `x-amz-trailer`, and verify it. Several value headers, or a value header naming another algorithm than the algorithm header, is 400 InvalidRequest. New exclusion (WP-6). |
| 7 | CompleteMultipartUpload accepts a supplied checksum that only starts with the computed one, `scm.compare(0, c_len, …)` (`src/rgw/rgw_op.cc:7317-7334@v20.2.4`). | S3-correctness, minor | unreproduced | Accept the exact armored value, or a composite's value without `-N` (radosgw's documented minio leniency). Anything else is BadDigest. New exclusion (WP-9). |
| 8 | ListParts never reports `ChecksumAlgorithm`: it reads the meta object's `user.rgw.cksum`, which no code writes (`src/rgw/rgw_rest_s3.cc:4695-4698@v20.2.4`, `src/rgw/rgw_op.cc:7617-7630@v20.2.4`). | S3-correctness, minor | unreproduced | Report `ChecksumAlgorithm` and `ChecksumType` from the upload's `multipart_upload_info`. New exclusion (WP-9, WP-13). |
| 9 | `Cksum::decode` copies a stored digest size of up to 65535 bytes into a 64-byte array (`src/rgw/rgw_cksum.h:268-281@v20.2.4`). A v20.2.4 gateway also indexes its 9-entry table with any stored type (`src/rgw/rgw_cksum.h:147-149@v20.2.4`), which main's types 9-11 overrun in a mixed v20/v21 zone. | security (memory safety), privileged: needs a RADOS writer of the attr, or a newer radosgw | unreproduced | Refuse a size other than the type's and a type above 8. Never write either. Registry entry; no exclusion (WP-1). |
| 10 | `?layout` ignores `dump_obj_layout`'s error and answers 200 with a truncated `{"result":{...}}` (`src/rgw/rgw_rest_s3.cc:4879-4896@v20.2.4`, `src/rgw/rgw_rest_s3.cc:4338-4355@v19.2.6`). | cosmetic | unreproduced | Answer the error. New exclusion (WP-21). |
| 11 | An otp put with `step_size` 0 makes every later check fault the OSD on a divide (registry, "cls_otp divides by a stored step_size that is never validated", tracker #80948). | security (DoS, admin-only) | reproduced in userspace only (registry) | Refuse `step_size` 0 with 400 InvalidArgument; update the registry's rgw-go line (WP-19). |
| 12 | An otp put removes and recreates the object (`src/rgw/services/svc_cls.cc:188-216@v19.2.6`), so every device's replay state (`last_checks`, `last_success`) restarts, and a TOTP just used can be used again within its window. | security, low (admin-triggered); intended by multisite's design | unreproduced | Reproduce: the op is radosgw's and the stored form is the class's. Record it in the registry as intended behaviour that surprises (WP-19). |

Not defects, recorded so no task re-derives them:
- `?layout` checks only the `admin` cap, by design.
- An otp put changes no user record. radosgw-admin's `mfa create` writes both the device and
  the user's `mfa_ids` (`src/rgw/rgw_admin.cc:10791-10856@v19.2.6`), so MFA through the
  metadata API needs both puts.
- A checksum algorithm header with no value and no trailer stores the computed checksum
  unverified, as AWS's SDK flow expects.

## 7. Proposed tasks

Twenty-four tasks, WP-1 to WP-24. Each task's brief cites the §2 lines it reproduces, and
writes its own `docs/exclusions.md` and `docs/ceph-upstream-bugs.md` entries (the standing
rule). "Cluster" marks the tasks that need a disposable cluster; every other task runs on
unit specs, fakerados, memstore and counterfeiter fakes. Brief lengths are estimates in lines.

### 7.1 Hot files and how to avoid serializing on them

| File | Tasks here | Other areas | Plan |
|---|---|---|---|
| `internal/op/object.go`, `internal/op/multipart.go`, `internal/op/objectread.go`, `internal/memstore/{object,multipart}.go` | WP-5 alone | versioning, lock-lifecycle (their own params) | WP-5 adds every field this area needs, in one PR, early. Other areas' additions are line-local fields. |
| `internal/driver/put.go`, `stripe.go` (`putStream`, `tailWriter.consume`) | WP-10, WP-8 | versioning V2 and lock-lifecycle L5 work in `headwrite.go`, not here | Fixed order: WP-10 PR (a) first splits the body into a plaintext stage, with its sinks, and a stored stage. WP-10 (b) and WP-8 then each add one stage, in that order. |
| `internal/driver/mp_part.go` (`registerPart`), `mp_upload.go` (`CreateUpload`), `mp_complete.go` | WP-8, WP-9, WP-10 | lock-lifecycle (object-lock fields in `multipart_upload_info`), versioning (versioned completion) | Order WP-10 → WP-8 → WP-9 on `mp_part.go`. `CreateUpload`'s info literal is line-local. On `mp_complete.go` the checksum combination is a new function in `mp_cksum.go`, called once from the assembly loop. |
| `internal/driver/metadata.go`, `internal/op/adminmetadata.go` | WP-18 (owner), WP-19 | phase 3's roles, user_policy, group and topic sections | WP-18 replaces the three switches and `metadataSections` with a table of section handlers, one file per section. WP-19 and phase 3 each add a file and a row. |
| `internal/s3/putobject.go`, `multipart.go`, `getobject.go`, `getobjattrs.go`, `copyobject.go` | WP-7, WP-13 | versioning, lock-lifecycle C8 and L5, bucketconfig BC-26 | Lock-lifecycle's order: versioning → this area's header tasks (WP-7, then WP-13) → L5 → C8. Each task adds its headers in its own helper in `internal/s3/checksum.go`, with one call line per site. |
| `internal/op/{putobject,uploadpart,initmultipart,completemultipart}.go` | WP-6 | bucketconfig BC-26 (encryption), versioning | WP-6's parsing lives in `internal/op/checksumreq.go`, one call per op. The controller merges BC-26 and WP-6 one after the other, never in parallel on the same op (bucketconfig §7.1). |
| `internal/cls/otp/`, `internal/testutil/fakerados/cls_otp*.go` | WP-16, WP-17 | versioning (check and get_result) | WP-16 creates the package with `types.go` and `ops.go`; versioning adds `ops_check.go`. WP-17 builds the emulator from a method table, with versioning's methods in `cls_otp_check.go`. |
| `hack/s3tests/run.sh`, `EXCLUDED_MARKERS`, `deselect-phase2.txt`, ledgers, known differences | WP-22, WP-23, WP-14 | every area's recording task | WP-22 moves the selection into Go first. Each area edits only its own lines, and recordings land one at a time through the controller. |
| `internal/auth/chunked.go`, `payload.go`; `internal/op/identity.go` | WP-3 alone | bucketconfig BC-31 adds a new file and does not edit these | single owner |

### 7.2 Seams shared with other areas

- **Versioning (MFA).**
  - WP-16 delivers `internal/cls/otp` with the v1 wire types `OTPInfo` (`otp_info_t`) and
    `OTPCheck` (`otp_check_t`), the result constants, and `OTPObject(uid meta.UserID) string`
    = `"user:" + uid.String()`. The object lives in `ZoneParams.OTPPool`.
  - Versioning adds `Check(id, pin, token)` and `GetResult(token)` in `ops_check.go`. They
    send the same `cls_otp_check_otp_op` buffer to both methods, as radosgw does, and the
    `otp_check` call goes in a write op (`src/cls/otp/cls_otp_client.cc:60-96@v19.2.6`).
  - WP-17's emulator stores `last_checks` and `last_success` per device and exposes them
    to versioning's check methods through the same table.
  - The otp metadata section (WP-19) writes the devices those checks read. The user's
    `mfa_ids` stay in the user section, which already carries them.
- **Lock-lifecycle (transition).** No new seam is needed:
  - `op.ZoneInfo.Placement(rule)` is `valid_placement` and returns the class's pools
    (`internal/op/zone.go:24-42`);
  - the driver's stored-byte `copyData` (`internal/driver/copy.go:431`) is `copy_obj_data`
    with no data-processor factory.
  This area's constraint on both: WP-11's compression of a streamed copy applies only to a
  client CopyObject. The transition keeps the raw path, with no compression or checksum
  filter, as `transition_obj` does (`src/rgw/driver/rados/rgw_rados.cc:5417-5487@v20.2.4#RGWRados::transition_obj`).
  WP-11 states that as a parameter of `copyData` that lifecycle's C11 passes.
- **Bucketconfig (POST object).** BC-32 builds its write through `op.PutParams.Checksum`
  (WP-5), from the form's `x-amz-checksum-<alg>` field. `cksum.ParseName` (WP-1) decides
  which fields are checksum fields, for BC-30's policy exception
  (`src/rgw/rgw_rest_s3.cc:3104-3116@v20.2.4`). Compression applies to POST through the
  driver's `PutObject` (WP-10), with nothing more to do in BC-32.
- **Bucketconfig and lock-lifecycle (route sets).** WP-21's `get_obj_layout` handler binds
  through BC-1's per-feature route sets (`internal/s3/layout.go`).

### 7.3 Checksums

**WP-1. Checksum types and record (library).**
- **Files.** New `internal/cksum/{doc.go,type.go,record.go}`, a leaf package that imports
  `denc` only.
- **Seams.**
  - `type Type uint16`, with Tentacle's nine ids;
  - `ParseName(string) (Type, bool)`: case-insensitive, Tentacle's names plus open
    question 1's AWS names;
  - `(Type).Info()`: name, header name, element name, digest size, AWS, CRC;
  - `type Cksum struct{ Type Type; Digest []byte; Flags uint16 }` with `Encode(*denc.Encoder)`,
    `Decode(*denc.Decoder) (Cksum, error)` and `Armor()`;
  - `ObjectType(multipart bool)`, `get_checksum_type`;
  - `PartType()`, main's `get_part_checksum_type`;
  - `FlagsOf(Type)`, `Permitted(Type, flags)`, and the flag constants.
- **Behaviour.** §2.1's "Types and stored form", every rule with the citation §2.1 gives it.
  A decode refuses a type above 8 and a size
  other than the type's (§6 #9), which the readers turn into "no checksum".
- **Specs.**
  - the type and flag tables as rows;
  - v1 and v2 records;
  - hand-built encodings from `Cksum::encode`;
  - each refusal;
  - `ObjectType` over every flag combination radosgw writes: 0, upload flags without V2,
    and the combined forms.
- **Depends on** nothing. One PR. About 150 lines.

**WP-2. Digests and combiners (library).**
- **Files.** `internal/cksum/{digest.go,combine.go}`.
- **Seams.** `NewHash(Type) (hash.Hash, error)`, whose `Sum` is the stored digest: Go's
  `hash/crc32` and `hash/crc64` already append big-endian. `Finish(Type, hash.Hash, flags)
  Cksum`. `NewCombiner(Type, flags) Combiner` with `Append(Cksum, partSize uint64) error`
  and `Final() Cksum`.
- **Behaviour.**
  - The CRCs:
    - CRC32 is IEEE;
    - CRC32C is Castagnoli;
    - CRC64NVME is `crc64.MakeTable` of the reflected NVMe polynomial: init and xorout all
      ones, as `hash/crc64` applies them.
  - The digests come from `crypto/sha1`, `sha256` and `sha512`; xxh3 and blake3 are per
    open question 1.
  - The combiner follows `src/rgw/rgw_cksum.cc:31-155@v20.2.4`, with two corrections:
    - a CRC upload whose flags say COMPOSITE gets the composite, a CRC of the concatenated
      part CRCs (§6 #2);
    - every combined CRC is stored `V2|COMBINED|FULL_OBJECT` (§6 #3).
  - The CRC combination is zlib's GF(2) operator method, sketched in the brief, generic over
    32 and 64 bits.
- **Specs.**
  - radosgw's vectors (`src/test/rgw/test_rgw_cksum.cc:106-143@v20.2.4`,
    `src/test/rgw/test_rgw_cksum.cc:440-467@v20.2.4`);
  - the s3-tests values of §5.3;
  - crc(a‖b) = combine(crc(a), crc(b), len(b)) over random splits and lengths 0, 1 and
    2^20+3;
  - the composite-CRC and single-part cases.
- **Depends on** WP-1. One PR. About 180 lines.

**WP-3. Trailer values (auth).**
- **Files.** `internal/auth/chunked.go`, `payload.go`; `internal/op/identity.go`.
- **Seam.** `op.AuthResult.Trailers`, `interface{ Trailer(name string) (string, bool) }`:
  nil for a payload with no trailer, and answering only after `Body` has returned its EOF.
- **Behaviour.**
  - radosgw puts each trailer `x-amz-trailer` names into the request's environment as it
    completes the body (`src/rgw/rgw_auth_s3.cc:1459-1488@v20.2.4`).
  - rgw-go parses the trailer section as lines, under its existing bound and exclusions
    ("aws-chunked trailer sections are read line by line", `docs/exclusions.md:1861-1919`),
    for the signed-trailer and the unsigned-trailer payloads alike. Today it parses only the
    signed (`internal/auth/chunked.go:395-398`).
  - The value is the first line bearing exactly the listed name.
- **Specs.** Both payloads; a listed name absent; a value read before EOF; the 1 KiB bound
  on the unsigned payload.
- **Depends on** nothing. One PR. About 120 lines.

**WP-5. Checksum and compression contract (op, memstore).**
- **Files.** `internal/op/{object,multipart,objectread}.go`, new `internal/op/checksum.go`,
  `internal/memstore/{object,multipart}.go`, the regenerated fakes, `internal/op/conformance`.
- **Seams** (additive):
  - `op.ChecksumRequest{Type cksum.Type; Flags uint16; Expected func() (string, bool)}`,
    where `Expected` is called once the body is read, so a trailer's value is there;
  - `PutParams.Checksum *ChecksumRequest`;
  - `PutResult.Checksum *cksum.Cksum`;
  - `UploadParams.Checksum` and `Upload.Checksum`, a `{Type, Flags}` pair;
  - `Upload.SuppliedChecksum *ChecksumRequest`, request-scoped like `Upload.WriteTag`, which
    avoids changing `Complete`'s signature;
  - `PartResult.Checksum` and `Part.Checksum`;
  - `ObjectState.Checksum *cksum.Cksum`, nil when absent or undecodable;
  - `ObjectPart.Checksum`.
- **Behaviour.** memstore computes, verifies and stores as the driver will. Its conformance
  cases run against both stores:
  - a verified checksum stored;
  - a mismatch is BadDigest with nothing written;
  - a part's type must match its upload's;
  - completion's combination over the listed parts.
- **Depends on** WP-1, and WP-2 for memstore's computation. One PR. About 200 lines.

**WP-6. Checksum requests (op).**
- **Files.** New `internal/op/checksumreq.go`; one call each in `putobject.go`,
  `uploadpart.go`, `initmultipart.go`, `completemultipart.go`.
- **Behaviour.** From the header values the handler passes:
  1. **The algorithm.** It is `x-amz-checksum-algorithm`, else
     `x-amz-sdk-checksum-algorithm` (radosgw's order), else the one `x-amz-checksum-<alg>`
     header, else the checksum `x-amz-trailer` names (§6 #6).
     - A name rgw-go does not serve is 400 InvalidArgument, radosgw's EINVAL
       (`src/rgw/rgw_cksum_pipe.cc:40-73@v20.2.4#rgw::putobj::RGWPutObj_Cksum::Factory`).
     - Two value headers, or a value header or trailer naming another algorithm than the
       algorithm header, is 400 InvalidRequest. That is new, and an exclusion.
  2. **The expected value.** It is that header's value, or the trailer's through
     `AuthResult.Trailers`.
  3. **A part.** It takes its upload's type and flags when it names no algorithm
     (`src/rgw/rgw_op.cc:4471-4472@v20.2.4`). A part naming another algorithm than its
     upload's is 400 InvalidRequest at UploadPart; radosgw fails it only at completion
     (`src/rgw/rgw_op.cc:7121-7131@v20.2.4`). That is an exclusion.
  4. **CreateMultipartUpload.** It takes `x-amz-checksum-type` and `Permitted` (400
     InvalidRequest) as `src/rgw/rgw_rest_s3.cc:4507-4521@v20.2.4`, and keeps radosgw's
     "unknown algorithm is none".
  5. **CompleteMultipartUpload.** Its supplied value is any `x-amz-checksum-<alg>` header,
     crc64nvme included (§6 #4).
  6. **Squid.** It follows open question 2: the upload's type is neither stored nor taken,
     and single-object checksums and a part's own supplied value are served.
  7. **Placement in the flow.** The refusals come where radosgw's Factory runs: once
     authorized, before the body.
- **Specs.** A table from headers to `ChecksumRequest` or error, per op and release; the
  error order against Content-MD5 and the body with fakes.
- **Depends on** WP-3, WP-5. One PR. About 200 lines.

**WP-7. Checksum headers on writes (s3).**
- **Files.** New `internal/s3/checksum.go`; one call each in `putobject.go` and
  `multipart.go`.
- **Behaviour.**
  - The request's header values go to WP-6's fields.
  - PutObject, UploadPart and UploadPartCopy answer `x-amz-checksum-type: FULL_OBJECT` and
    the value header for an AWS type (`src/rgw/rgw_rest_s3.cc:2891-2925@v20.2.4#RGWPutObj_ObjStore_S3::send_response`).
  - CreateMultipartUpload answers `x-amz-checksum-algorithm` upper-cased
    (`src/rgw/rgw_rest_s3.cc:4540-4542@v20.2.4`).
  - CompleteMultipartUpload answers the `Checksum<ALG>` and `ChecksumType` elements, with
    `-N` for a composite (`src/rgw/rgw_rest_s3.cc:4635-4640@v20.2.4`).
  - Names follow open question 1.
- **Specs.** Each response, per release; no header for a non-AWS type.
- **Depends on** WP-6. One PR. About 140 lines.

**WP-8. PUT and part checksums (driver).**
- **Files.** `internal/driver/put.go`, `mp_part.go`, `stat.go`.
- **Behaviour.**
  1. A plaintext sink hashes the body beside the MD5 (WP-10's stage).
  2. After the Content-MD5 check and before the head write, the request's `Expected` value
     is compared; a mismatch is BadDigest with the tails discarded
     (`src/rgw/rgw_op.cc:4757-4790@v20.2.4`).
  3. `user.rgw.cksum` is set with radosgw's flags: 0 for an object, the upload's flags for a
     part.
  4. A part's record carries the checksum at version 6 on Tentacle.
  5. `CopyPart` hashes with its upload's type through the same path.
  6. `StatObject` decodes the attr into `ObjectState.Checksum`.
- **Specs.** On fakerados:
  - the attr's bytes;
  - nothing written on a mismatch;
  - the part record on each release;
  - a stat of a v1 record, a type-9 record and a truncated one.
- **Depends on** WP-2, WP-5, WP-10. One PR. About 180 lines.

**WP-9. Multipart checksums (driver).**
- **Files.** `internal/driver/mp_upload.go`, `mp_list.go`, `mp_complete.go`, new
  `mp_cksum.go`.
- **Behaviour.**
  - **CreateUpload** writes the type and flags into `multipart_upload_info` v4 on Tentacle,
    and v2 without them on Squid (`src/rgw/rgw_common.h:1552-1592@v20.2.4`). **GetUpload**
    decodes them, and ListParts returns each part's checksum.
  - **Complete:**
    - It combines exactly the parts the request names, from the part records the assembly
      already reads (§6 #1). A part without a checksum, or of another type, is 400
      InvalidRequest (`src/rgw/rgw_op.cc:7105-7131@v20.2.4`).
    - It compares the supplied value, exact or a composite without `-N` (§6 #7), as BadDigest
      before the head write, under the completion lock.
    - It sets the combined attr.
    - A retried completion returns the stored checksum (`src/rgw/rgw_op.cc:7474-7501@v20.2.4`).
- **Specs.**
  - two- and three-part uploads, composite and full-object, on fakerados;
  - an upload with an extra part the request omits;
  - a part uploaded through "radosgw", hand-written records at radosgw's flags;
  - the release split.
- **Depends on** WP-2, WP-5, WP-8. One PR. About 200 lines.

**WP-12. Checksum rules on reads (op).**
- **Files.** `internal/op/getobject.go`, `objectread.go`, `listparts.go`.
- **Behaviour.**
  - **Whether to show one.** GET and HEAD show it when `x-amz-checksum-mode` equals
    `enabled` case-insensitively and there is no Range (`src/rgw/rgw_rest_s3.cc:314-318@v20.2.4`,
    `src/rgw/rgw_rest_s3.cc:548-586@v20.2.4`).
  - **The whole object.** It takes `ObjectType` with the parts count; a composite shows `-N`.
  - **A `partNumber` read.** It takes the part head's checksum through `PartType`, bare (§6
    #5, main's rule).
  - **A composite on an object without parts** shows nothing. That is the case of a
    multipart source a streamed copy carried (§4); it is an exclusion.
  - **GetObjectAttributes.** Its `Checksum` follows the same rules. Each part's checksum
    comes from the part head it already stats, never from another part: the registry's
    ObjectParts entry, which rgw-go does not reproduce.
  - **ListParts.** It takes `ChecksumAlgorithm` and `ChecksumType` from the upload (§6 #8).
  - **Squid.** It shows what it stores, as for GetObjectAttributes (owner decision 11).
- **Specs.** Each rule with fakes.
- **Depends on** WP-5, WP-8, WP-9. One PR. About 150 lines.

**WP-13. Checksum rendering on reads (s3).**
- **Files.** `internal/s3/checksum.go`, `getobject.go`, `getobjattrs.go`, `multipart.go`.
- **Behaviour.** Headers `x-amz-checksum-<alg>` or `x-rgw-checksum-<alg>`, and
  `x-amz-checksum-type`. GetObjectAttributes' `<Checksum>` with `Checksum<ALG>` and
  `ChecksumType`, and each part's element (`src/rgw/rgw_rest_s3.cc:4029-4057@v20.2.4`,
  `src/rgw/rgw_rest_s3.cc:4083-4096@v20.2.4`). ListParts' elements
  (`src/rgw/rgw_rest_s3.cc:4695-4713@v20.2.4`).
- **Specs.** Bodies byte-compared with xmltext.
- **Depends on** WP-12. One PR. About 120 lines.

**WP-14. Checksum parity (T; cluster).**
- **Behaviour.**
  - Delete `deselect-phase2.txt:130-131`, and drop `checksum` from `EXCLUDED_MARKERS`.
  - Deselect `test_post_object_upload_checksum` until BC-34 lands.
  - Record both ledgers for the freed tests, with `make s3tests-record-tests`, and write the
    `known-differences-{squid,tentacle}.txt` lines §5.1 predicts, each under its exclusion.
  - Say from the run's freeze whether botocore sends checksums on every PUT.
  - Capture `user.rgw.cksum` vectors from radosgw-written objects for WP-1's goldens.
  - Reproduce §6 #1-#8 on Tentacle and record each result in the registry.
- **Depends on** WP-6 to WP-13. One PR. About 120 lines.

### 7.4 Compression

**WP-4. Block encoders (library).**
- **Files.** New `internal/compression/encode.go`.
- **Seams.** `NewEncoder(codec string, o EncoderOptions) (Encoder, error)`, with `o` holding
  the zlib level, the zlib winsize and the zstd level. `(Encoder).Encode(dst, src []byte)
  (block []byte, message *int32, err error)`. An unknown codec is `ErrUnknownCodec`.
- **Behaviour.** Each codec's Ceph framing (§2.2):
  - zlib: byte 0, then `compress/flate` at the level.
    - Message -15 for a negative winsize: raw deflate.
    - 15 for a winsize of 8 to 15: the zlib wrapper.
    - 31 above 15: gzip.
    - Never a narrower window than the 32 KiB Go's deflate uses (§4).
  - zstd: a le32 length, then a klauspost frame at the level mapped from
    `compressor_zstd_level`.
  - lz4: count 1, the pair, then a pierrec/lz4 block.
  - snappy: a klauspost snappy block.
- **Specs.**
  - each codec round-trips through phase 1's `NewDecoder`;
  - the framing bytes;
  - the message per winsize, including a winsize of 8, which the registry's "compressor_zlib_winsize 8" entry
    names, written as 15;
  - an empty input.
- **Depends on** nothing (no new dependency: klauspost/compress and pierrec/lz4/v4 are already
  in §4). One PR. About 140 lines.

**WP-10. Compression on PUT and parts (driver).**
- **Files.** `internal/driver/put.go`, `stripe.go`, `mp_part.go`, `headwrite.go` (the hint),
  `writer.go` (options).
- **Behaviour.**
  - **Choosing a compressor.** The placement is the destination's
    (`op.Placement.Compression`). `random`, and a codec rgw-go cannot load, store the object
    uncompressed and log once, as radosgw's missing plugin does
    (`src/rgw/rgw_op.cc:4563-4587@v20.2.4`). `random` is a teuthology setting; it is an
    exclusion.
  - **Blocks.** The plaintext is cut into `rgw_max_chunk_size` blocks, each compressed as
    one block (`src/rgw/rgw_compression.cc:44-88@v20.2.4#RGWPutObj_Compress::process`). A
    first block that fails stores the object raw; a later one is 500.
  - **What the tail writer sees.** It receives the compressed stream, so the head and the
    stripes are cut from it.
  - **What stays on the plaintext.** The MD5, the declared length, `rgw_max_put_size`, both
    quota checks and WP-8's checksum.
  - **The attr.** `user.rgw.compression` is the `RGWCompressionInfo` of
    `src/rgw/rgw_op.cc:4686-4695@v20.2.4`. The index entry's accounted size is the
    plaintext size.
  - **Hints.** INCOMPRESSIBLE (512) on every tail write and on the head's `write_full` of a
    compressed object (`src/rgw/driver/rados/rgw_putobj_processor.cc:146-155@v20.2.4#RadosWriter::add_write_hint`,
    `src/rgw/driver/rados/rgw_rados.cc:3326-3335@v20.2.4`).
  - **Parts.** Each part compresses on its own, and its record carries its `cs_info`
    (`src/rgw/driver/rados/rgw_putobj_processor.cc:586-591@v20.2.4`). UploadPartCopy gets
    the same through `PutPart`.
  - **Exclusion.** Phase 1's silent difference, a PUT into a compressing class stored raw,
    closes. Any zlib-winsize mixed-upload refusal (§4) becomes an exclusion.
- **Specs.** On fakerados:
  - the head and tail layout of a 10 MiB PUT into each codec's class;
  - the block map;
  - the hint steps;
  - a ranged GET of an rgw-go-written object through phase 1's reader;
  - a part's record;
  - a mixed upload refused at completion by phase 1's merge;
  - the plaintext checks with a body cut short.
- **Depends on** nothing for PR (a); WP-4 for PR (b).
- **PR split.** (a) The body split into a plaintext stage with sinks and a stored stage, plus
  the hint plumbing, with no behaviour change, so WP-8 can start on it. (b) Compression.
- About 230 lines.

**WP-11. Copies (driver).**
- **Files.** `internal/driver/copy.go`.
- **Behaviour.**
  - **A streamed copy of an uncompressed source.** Into a placement with a compression type,
    it compresses through WP-10's stage, as Tentacle's `RGWCopyObjDPF` does
    (`src/rgw/rgw_op.cc:5825-5884@v20.2.4`), on both releases (open question 3).
  - **A compressed source.** It stays a raw copy (owner decision 4).
  - **`copyData`'s `raw bool` parameter.** Lifecycle's transition passes true, which skips
    every filter, `transition_obj`'s nil factory.
  - **The checksum attr.** It follows `set_copy_attrs`: kept under COPY, dropped under
    REPLACE (`src/rgw/driver/rados/rgw_rados.cc:3858-3885@v20.2.4#set_copy_attrs`).
    A composite checksum is also dropped when the copy streams to a destination with no
    parts, rather than left to be read as FULL_OBJECT. That is an exclusion.
- **Specs.**
  - the four source and destination combinations on fakerados;
  - the raw flag;
  - the attr carried and dropped.
- **Depends on** WP-10, WP-8. One PR. About 130 lines.

**WP-15. Compression and checksum gate (integration; cluster).**
- **Files.** New `test/integration/phase2_writepath_test.go`, labelled
  `integration, rgw-go, phase2, writepath`.
- **Behaviour.** §4's gate cases 1 to 5 on both releases, the Tentacle-only cases skipped on
  Squid. It reads radosgw's view through `radosgw-admin object stat`, and
  `bucket check` must come back clean.
- **Depends on** WP-8 to WP-11 and WP-13. One PR. About 160 lines.

### 7.5 `?layout`

**WP-20. Object layout (driver).**
- **Files.** New `internal/driver/layout.go`; new `op.LayoutStore` interface
  (`internal/op/layout.go`), with memstore and the fakes.
- **Seam.** `LayoutStore.ObjectLayout(ctx, st *op.ObjectState) (json.RawMessage, error)`.
  It is kept off `ObjectStore` so that the hot interface is not touched.
- **Behaviour.** The `result` document of `RadosObject::dump_obj_layout`
  (`src/rgw/driver/rados/rgw_sal_rados.cc:3615-3661@v20.2.4#RadosObject::dump_obj_layout`):
  - `head`, the raw object from the driver's head ref;
  - `manifest`, `meta.Manifest.MarshalJSON`, with v19.2.6's tier_config rule on Squid;
  - `data_location` from the manifest walk, with `loc_size` capped at what is left.
  The JSON is ceph's formatter's: keys in this order, no spaces.
- **Specs.** Documents for the populated corpus' shapes: in-head, striped, multipart,
  compressed, `_underscore`. Golden bytes come from WP-24's capture.
- **Depends on** nothing. One PR. About 140 lines.

**WP-21. GET `?layout` (op and handler).**
- **Files.** New `internal/op/layout.go` (the op), new `internal/s3/layout.go` (BC-1 route
  set).
- **Behaviour.**
  - **Permission.** Name `get_obj_layout`, op mask READ. VerifyPermission is
    `CheckCaps(r, "admin", read)` alone (`src/rgw/rgw_op.h:2537-2554@v20.2.4#RGWGetObjLayout`).
  - **A missing object** follows the existing missing-object rule.
  - **The response.** 200 `application/json`, with a Content-Length of the body and no
    Accept-Ranges (`src/rgw/rgw_client_io_filters.h:208-236@v20.2.4`).
  - **A layout error** is answered as an error (§6 #10); it is an exclusion.
  - **The exclusions.** The `?layout` mention in "Tentacle's x-amz-delete-marker..." and the
    501 go.
- **Specs.** The cap refusal, a missing object, the body, and HEAD `?layout` staying a HEAD
  object.
- **Depends on** WP-20, BC-1. One PR. About 110 lines.

### 7.6 Admin otp

**WP-16. `internal/cls/otp` (cls).**
- **Files.** `internal/cls/otp/{doc.go,types.go,ops.go}`.
- **Seams.**
  - `OTPInfo` (`otp_info_t` v1) with its JSON as `dump` and `decode_json` write and read it
    (`src/cls/otp/cls_otp_types.cc:30-71@v19.2.6`);
  - `OTPCheck`;
  - `OTPObject(uid)`;
  - `Set(w *radosclient.WriteOp, devs []OTPInfo)`, an `otp_set` exec;
  - `GetAll(r *radosclient.ReadOp)` with its reply decoder, `otp_get` with `get_all`;
  - `Remove(w, ids)`;
  - `GetCurrentTime`.
- **Behaviour.** `src/cls/otp/cls_otp_ops.h:11-167@v19.2.6` and
  `src/cls/otp/cls_otp_client.cc:33-188@v19.2.6`, identical at v20.2.4.
- **Specs.** Request bytes for each method; reply decoding; the JSON round trip, `seed_bin`
  never shown.
- **Depends on** nothing. One PR. About 120 lines.

**WP-17. otp class emulator (fakerados).**
- **Files.** New `internal/testutil/fakerados/cls_otp.go`.
- **Seams.** `OTPClass()` built from `otpMethods`, a table merged from `otpAdminMethods`
  (here) and `otpCheckMethods` (an empty table here, which versioning fills in
  `cls_otp_check.go`). `OTPWriteMethods` is derived from the table's WR flags.
- **Behaviour.**
  - `otp_set`, `otp_get` and `otp_remove`, `get_current_time`;
  - the omap `header` and `otp/<id>` instances, with `last_checks` and `last_success`
    kept;
  - EINVAL on a request that does not decode, or a seed that does not parse
    (`src/cls/otp/cls_otp.cc:268-445@v19.2.6`).
- **Specs.** Each method; a set preserving a device's replay state; `get_all` against `ids`.
- **Depends on** WP-16. One PR. About 140 lines.

**WP-18. Metadata sections as a table (driver, op).**
- **Files.** `internal/driver/metadata.go` and one file per existing section
  (`metadata_user.go`, `metadata_bucket.go`, `metadata_instance.go`);
  `internal/op/adminmetadata.go`.
- **Seam.** `type metadataSection interface{ Get; Put; Remove; Pool and keyOf }`,
  registered by name. `op.metadataSections` is derived from the driver's names through
  `MetadataStore` (a new `Sections() []string`).
- **Behaviour.** Unchanged.
- **Specs.** Today's specs pass unchanged. An unknown section stays ErrNoSuchKey.
- **Depends on** nothing. One PR. About 100 lines.

**WP-19. The otp section (driver).**
- **Files.** New `internal/driver/metadata_otp.go`, with one row in WP-18's table.
- **Behaviour.** §2.5:
  - **get** is one read op with stat, a cls_version read and `otp_get`. A missing object is
    ErrNoSuchKey.
  - **put** is one write op: remove (FAILOK), create(false), the version set from the
    document's `ver` (a fresh random tag at 1 when the tag is empty), `mtime2` from its
    mtime, and `otp_set`.
  - **remove** goes under the version read.
  - **list** lists every object of `OTPPool`.
  - **The status a put reports.** Squid's 204 `applied`, Tentacle's 200 with no status
    header, and `RGWX_UPDATE_VERSION: ver:0,tag:` on both.
  - **update-type.** rgw-go applies it as its other sections do. radosgw applies every otp
    put; the "admin metadata API" exclusion widens to say so.
  - **Refused with 400 InvalidArgument before any write:**
    - a `step_size` of 0, which takes the registry's #80948 out of reach;
    - a type other than TOTP and a seed type other than hex or base32, as `radosgw-admin
      mfa create` refuses them (`src/rgw/rgw_admin.cc:10810-10817@v19.2.6`);
    - a NUL in the key.
  - **Exclusions.** The "serves three sections" exclusion and the registry's rgw-go lines
    for #80948 and #80949 change.
- **Specs.** On fakerados with WP-17: each verb, each refusal, the release split of the put
  status, and a list across pages.
- **Depends on** WP-16, WP-17, WP-18. One PR. About 170 lines.

**WP-24. `?layout` and otp gate (integration; cluster).**
- **Files.** New `test/integration/phase2_layout_otp_test.go`.
- **Behaviour.** §4's gate cases 6 and 7:
  - rgw-go's and radosgw's `?layout` bodies byte-equal for every populated object and for
    rgw-go-written ones, which also yields WP-20's goldens;
  - `radosgw-admin mfa create`, `mfa list` and `mfa check` against rgw-go's otp section, and
    the reverse.
- **Depends on** WP-19, WP-21. One PR. About 120 lines.

### 7.7 Gate tooling

**WP-22. The s3-tests selection in Go (T).**
- **Files.** `hack/parity` (a `select` command), or a new small `hack/s3tests` Go command;
  `hack/s3tests/run.sh` shrinks to glue.
- **Behaviour.** Everything `run.sh:65-345` decides, moved without change:
  - the marker expression;
  - the deselect lists' parsing, their prefix collision check and their stale-line check;
  - the TESTS-file subset;
  - the `--print-deselect` hash.
  The collected ids come from pytest `--collect-only` output that `run.sh` passes in. This
  follows the owner's rule against growing a large script.
- **Specs.** Each refusal `run.sh` makes today, as table rows; the hash unchanged for the
  current lists.
- **Depends on** nothing. One PR. About 150 lines.

**WP-23. The include list (T).**
- **Files.** The `select` command; new `hack/s3tests/include-phase2.txt`.
- **Behaviour.**
  - The set is (marker-selected ∪ include) − deselect.
  - An include line must name a collected test that the markers exclude; any other line is
    stale and refused.
  - pytest runs the explicit node ids without `-m`, so the run stays one junit report.
  - The hash covers the include list too.
  - The file starts empty, with bucketconfig's six encryption-configuration tests as its
    first entries. They land with BC-27, which owns them.
- **Specs.** Union, stale, overlap with a deselect line, and the hash.
- **Depends on** WP-22. One PR. About 100 lines.

**The go-ceph rgw/admin suite.** There is no task here. The two entries in
`hack/admin/deselect-phase2.txt` are versioning's and lock-lifecycle's. Each area's recording
task deletes its own line and runs `make admin-record`, as the file's header says. This area
has no admin-suite case.

### 7.8 Order

| Wave | Tasks | Waits for |
|---|---|---|
| 1, in parallel | WP-1, WP-3, WP-4, WP-10(a), WP-16, WP-18, WP-20, WP-22 | - |
| 2 | WP-2, WP-10(b), WP-17, WP-23 | WP-1; WP-4 and WP-10(a); WP-16; WP-22 |
| 3 | WP-5, WP-19, WP-21 | WP-2; WP-17 and WP-18; WP-20 and BC-1 |
| 4 | WP-6, WP-8, WP-24 | WP-3 and WP-5; WP-5 and WP-10(b); WP-19 and WP-21 |
| 5 | WP-7, WP-9, WP-11 | WP-6; WP-8; WP-8 and WP-10(b) |
| 6 | WP-12, then WP-13 | WP-9; versioning's header task before WP-13 (§7.1) |
| 7, one at a time | WP-15, WP-14 | everything above; WP-14 after bucketconfig's BC-34, or with the POST line deselected |

Wave 1 is wider than the three implementers phase 1 ran at once; the controller queues it
under the session's subagent budget, starting with WP-10(a), WP-1 and WP-22, which unblock the
most.

Cluster tasks: WP-14, WP-15, WP-24.

## 8. Open questions

1. **Which checksum algorithms does rgw-go accept on a write?** The stored type ids are fixed
   by Tentacle's nine-entry table. The s3-tests pin, ceph main, expects AWS's newer set.
   **Recommended:**
   - Accept CRC32, CRC32C, CRC64NVME, SHA1 and SHA256, Tentacle's AWS set.
   - Accept SHA512 under its AWS name. It is the standard library, and its type id 6 is one a
     Tentacle radosgw decodes, rendering it `x-rgw-checksum-sha512`.
   - Accept XXHASH3 (type 3, Tentacle's `xxh3`) only if the owner takes an XXH3 dependency
     (§4's list is closed; `cespare/xxhash` is XXH64 only). Otherwise refuse it.
   - Refuse MD5, XXHASH64 and XXHASH128, whose ids 9-11 make a v20.2.4 radosgw read past its
     table, and BLAKE3, which is radosgw's own extension, not AWS's, and needs a dependency.
     Each refusal is 400 InvalidArgument, radosgw's answer for a name it does not know.
   - Read and render any stored type 0-8, since rendering needs no computation. An UploadPart or a
     completion of a radosgw-begun upload of a refused type answers 501, an exclusion.

   Reason: the AWS-correct set where the stored form stays safe for a Tentacle radosgw.
2. **Checksums on Squid?** §8 makes a feature whose persistent form is a separate attribute
   available on Squid. A multipart upload's algorithm and a part record's checksum are
   struct fields, so they are not. **Recommended:**
   - On Squid, serve single-object checksums (PutObject, POST) and verify a part's own
     supplied value, storing `user.rgw.cksum` on heads and part heads.
   - Ignore CreateMultipartUpload's algorithm there, as Squid's radosgw does, so no combined
     checksum is stored.
   - Record the Squid known differences (§5.1).

   Reason: §8's rule applied as written, and AWS's verification where Squid's radosgw skips
   it.
3. **Compress a streamed copy of an uncompressed source?** Owner decision 4 made a
   compressed source's copy raw, and its exclusion says rgw-go, like Squid, stores an
   uncompressed source's streamed copy uncompressed. That was because phase 1 could not
   compress. **Recommended:** compress it into a compressing placement on both releases, as
   Tentacle's radosgw does (WP-11), and keep decision 4 for compressed sources. Reason: it
   is the write path phase 2 adds, and either gateway reads both forms.
4. **Tentacle's `BucketIndex` in CreateBucketConfiguration** (index type and `NumShards`,
   `src/rgw/rgw_rest_s3.cc:2651-2683@v20.2.4`) answers 501 and is in no phase.
   **Recommended:** leave it 501 in phase 2 with its existing exclusion, and list it under
   phase 3's remaining services if the owner wants it. Reason: §9 does not name it, and
   indexless buckets interact with listing, versioning and lifecycle beyond this area.
5. **What does §9's "`?layout` extension" mean?** This area's brief described it as a GET of
   a bucket's index layout. At both tags `?layout` exists only as an object GET,
   `RGWGetObjLayout`, whose body is the head, the manifest and the stripe locations, for a
   user with `admin=read` (§2.4). There is no bucket-scope `layout`. **Recommended:** plan
   WP-20 and WP-21 as the object op, and correct the brief's wording. Reason: it is the only
   `layout` radosgw has.
