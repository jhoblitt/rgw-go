package s3

import (
	"context"
	"crypto/md5" //nolint:gosec // Content-MD5 is an MD5
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// The object write routes are rgw_rest_s3.cc's RGWPutObj_ObjStore_S3,
// RGWDeleteObj_ObjStore_S3, RGWDeleteMultiObj_ObjStore_S3,
// RGWCopyObj_ObjStore_S3, RGWPutACLs_ObjStore_S3 at object scope,
// RGWPutObjTags_ObjStore_S3 and RGWDeleteObjTags_ObjStore_S3.

// putObject is put_obj without a copy source (RGWPutObj_ObjStore_S3,
// get_params
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2597-L2704,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2758-L2865;
// send_response
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2730-L2782,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2891-L2952).
// A request whose uploadId is not empty is a part, which uploadPart serves;
// RGWPutObj::execute takes an empty one for none
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4214 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4423 at v20.2.4).
// A PUT naming a copy source that is no copy is not served yet, and an
// append is refused by the op once the requester is authorized. The op
// reads the request's parameters through putObjectParams once the bucket is
// loaded, as init_processing reads them, and makes the checks of stored
// state through putObjectChecks once the requester is authorized, so that a
// refused requester learns nothing the store holds (docs/differences.md). The
// success is send_response's: the ETag, the Content-Length of 0 with
// Accept-Ranges that dump_content_length sends
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2747,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2911),
// x-amz-version-id (setVersionHeaders), x-amz-expiration
// (setWriteExpiration), the checksum the store computed
// (setChecksumHeaders), Rgwx-Mtime for a system request, and the status
// rgw_s3_success_create_obj_status names. The checksum request is read from
// the headers (checksumField).
func (wr *objectWrites) putObject(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	switch {
	case r.Query.Get("uploadId") != "":
		return wr.uploadPart(ctx, w, r)
	case hasHeader(r, "X-Amz-Copy-Source"):
		return fmt.Errorf("%w: a PUT that names a copy source but is not a copy", op.ErrNotImplemented)
	}
	o := &op.PutObject{
		Body: r.Body, Size: r.ContentLength,
		StorageClass: header(r, "X-Amz-Storage-Class"), CannedACL: header(r, "X-Amz-Acl"),
		IfMatch: headerPtr(r, "If-Match"), IfNoneMatch: headerPtr(r, "If-None-Match"),
		Append: r.Query.Has("append"), ChecksumField: checksumField(r),
	}
	o.Params = func(_ context.Context, o *op.PutObject) error { return wr.putObjectParams(r, o) }
	o.Authorized = func(ctx context.Context, o *op.PutObject) error { return putObjectChecks(ctx, r, o) }
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	SetCommonHeaders(w, r)
	h := w.Header()
	SetHeader(h, "ETag", `"`+o.ETag+`"`)
	SetContentLength(h, 0)
	setVersionHeaders(h, o.VersionID, "")
	setWriteExpiration(h, r, o.Tags, o.Mtime)
	setChecksumHeaders(h, o.Checksum)
	setSystemMtime(h, r, o.Mtime)
	w.WriteHeader(successStatus(r))
	return nil
}

// putObjectParams is the part of get_params that reads the request alone,
// in its order, and then the two checks RGWPutObj::execute makes of the
// request alone: a body without a Content-Length that is not chunked is
// MissingContentLength; a request that asks for encryption is refused, as
// are names that do not go together and an encryption header sent with no
// value (checkEncryption); x-amz-tagging that set_from_string refuses is
// InvalidArgument, as radosgw answers any such refusal on a PUT
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2626-L2637,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2787-L2798);
// the object-lock headers' retention and legal hold (objectLockHeaders),
// which r.Lock gives the policies' object-lock keys before the op authorizes;
// a partNumber is read as for a part (partNumberArg); an append's position
// (appendPosition); a Content-MD5 that
// ceph_unarmor does not decode to 16 bytes is InvalidDigest
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4184-L4198 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4393-L4407 at v20.2.4);
// and the attrs are requestAttrs' with the retention's and the legal
// hold's (requestLock.addAttrs). radosgw checks the last two after
// authorizing, and the attrs once the body is stored (docs/differences.md).
func (wr *objectWrites) putObjectParams(r *op.Request, o *op.PutObject) error {
	if r.ContentLength == 0 && r.Header.Get("Content-Length") == "" {
		return op.ErrMissingContentLength
	}
	if err := checkEncryption(r, true); err != nil {
		return err
	}
	if v, ok := op.HeaderValue(r.Header, "X-Amz-Tagging"); ok {
		set, terr := tags.ParseHeader(v, tags.MaxObjectTags)
		if terr != nil {
			return op.ErrInvalidArgument
		}
		o.Tags = &set
	}
	lock, lerr := objectLockHeaders(r, false)
	if lerr != nil {
		return lerr
	}
	r.Lock = lock.conditions()
	if _, err := partNumberArg(r); err != nil {
		return err
	}
	if o.Append {
		if err := appendPosition(r); err != nil {
			return err
		}
	}
	if v, ok := op.HeaderValue(r.Header, "Content-MD5"); ok {
		sum, ok := cephUnarmor(rgwtext.CString(v))
		if !ok || len(sum) != md5.Size {
			return op.ErrInvalidDigest
		}
		o.ContentMD5 = sum
	}
	attrs, err := requestAttrs(r, wr.generic, true)
	if err != nil {
		return err
	}
	lock.addAttrs(attrs)
	o.Attrs = attrs
	return nil
}

// partNumberArg is get_params' read of partNumber beside uploadId
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2682-L2694 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2835-L2847 at v20.2.4),
// which every request put_obj serves makes, a part or not. A value that is
// not empty is strict_strtol's in base 10, read as the C string get_params
// passes it, and one it refuses is InvalidArgument. An empty or absent one
// is InvalidArgument beside an upload id that is not empty, and otherwise 0,
// no part number. A number without an upload id is no part:
// RGWPutObj::execute takes the request for a PutObject
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4214 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4423 at v20.2.4)
// and reads the number only for a part.
func partNumberArg(r *op.Request) (int, error) {
	v := r.Query.Get("partNumber")
	if v == "" {
		if r.Query.Get("uploadId") != "" {
			return 0, fmt.Errorf("%w: an upload id without a part number", op.ErrInvalidArgument)
		}
		return 0, nil
	}
	n, err := parseInt(rgwtext.CString(v))
	if err != nil {
		return 0, fmt.Errorf("%w: part number %q: %w", op.ErrInvalidArgument, v, err)
	}
	return n, nil
}

// appendPosition is get_params' read of an append's position
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2696-L2708 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2849-L2861 at v20.2.4):
// strict_strtoll's in base 10, read as the C string get_params passes it;
// one it refuses, an absent one among them, or a negative one is
// InvalidArgument.
func appendPosition(r *op.Request) error {
	v := r.Query.Get("position")
	if n, ok := rgwtext.StrictStrtoll(rgwtext.CString(v)); !ok || n < 0 {
		return fmt.Errorf("%w: append position %q", op.ErrInvalidArgument, v)
	}
	return nil
}

// setVersionHeaders sends the version ids a write reports, each when it has
// one (op.ResponseVersionID): x-amz-version-id for the version written and,
// for a copy, x-amz-copy-source-version-id for the version it read.
// radosgw's PutObject and CompleteMultipartUpload send only a non-empty
// instance, so none for the null version, and its CopyObject sends neither
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2756,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4111 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3596-L3633 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2912,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4609 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3846-L3886 at v20.2.4;
// docs/differences.md, "Writes name the null version's id and a copy's two
// versions").
func setVersionHeaders(h http.Header, written, source string) {
	if written != "" {
		SetHeader(h, "x-amz-version-id", written)
	}
	if source != "" {
		SetHeader(h, "x-amz-copy-source-version-id", source)
	}
}

// putObjectChecks is the rest of get_params in its order, once the requester
// is authorized: the ACL is create_s3_policy's for the requester, whose
// grantees are looked up and whose bucket-owner grants name the bucket's ACL
// owner
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2618-L2620,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2779-L2781);
// then a retention or a legal hold on a bucket without object lock is
// refused (objectLock). radosgw makes both before verify_permission
// (docs/differences.md).
func putObjectChecks(ctx context.Context, r *op.Request, o *op.PutObject) error {
	p, err := newObjectACL(ctx, r)
	if err != nil {
		return err
	}
	o.ACL = p
	return objectLock(r)
}

// newObjectACL is create_s3_policy for a new object or copy
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2408-L2422,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2524-L2538):
// the requester's ACL owner, and for the bucket-owner canned ACLs the owner
// of the bucket's ACL, s->bucket_owner. Its errors are mapped through
// authz.ErrorFor.
func newObjectACL(ctx context.Context, r *op.Request) (acl.Policy, error) {
	bucketACL, err := op.BucketACLFor(ctx, r.BucketRec)
	if err != nil {
		return acl.Policy{}, err
	}
	res := authz.UserResolver{Users: r.Env.Users, Accounts: r.Env.Accounts}
	p, err := authz.DefaultACL(ctx, r, res, aclOwner(r.Identity), bucketACL.Owner)
	if err != nil {
		return acl.Policy{}, authz.ErrorFor(err)
	}
	return p, nil
}

// cryptRequest is a write's encryption headers and query parameters as
// radosgw files them: the names of crypt_attribute_map, which
// req_info::init_meta_info fills from the headers whose x_meta_map name
// starts x-amz-server-side-en, and map_qs_metadata from the query parameters
// whose lowercased name starts x-amz-server-side-encryption when query is set
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L413-L464 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L426-L477 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2589-L2603 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2742-L2756 at v20.2.4).
// Beast files a header sent with no value as present
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_client.cc#L36-L65 at v19.2.7
// and v20.2.4), so such a name is in the map.
//
// radosgw's environment keeps the last value of a header sent twice
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_env.cc#L22-L25 at v19.2.7
// and v20.2.4), and the query's parameter overrides the header's. rgw-go
// judges every instance instead, so that an empty one sent after a value
// never hides it, and a value anywhere asks for encryption.
type cryptRequest struct {
	names map[string]struct{}
	// asked is whether any instance of a header or parameter holds a value:
	// any of the names above, or a header naming a copy source's SSE-C key.
	asked bool
	// empty holds the message (emptyMessage) of each name an instance of
	// which holds no value and which emptyRefusal refuses so.
	empty map[string]struct{}
}

// cryptRequestOf reads every instance of r's encryption headers and, when
// query is set, every pair of its query string.
func cryptRequestOf(r *op.Request, query bool) cryptRequest {
	c := cryptRequest{names: map[string]struct{}{}, empty: map[string]struct{}{}}
	for name, values := range headerMetaValues(r.Header) {
		if isCryptName(name) {
			if strings.HasPrefix(name, cryptPrefix) {
				c.names[name] = struct{}{}
			}
			noteCrypt(&c, name, values...)
		}
	}
	if query {
		for k, v := range argPairs(r.RawQuery) {
			low := lowerASCIIString(k)
			if !strings.HasPrefix(low, cryptQueryPrefix) {
				continue
			}
			c.names[low] = struct{}{}
			noteCrypt(&c, low, v)
		}
	}
	return c
}

// noteCrypt records the values sent under name in c: one with a value asks for
// encryption, and one with none under a name emptyMessage covers is the empty
// header emptyRefusal refuses. An empty copy-source name is neither.
func noteCrypt(c *cryptRequest, name string, values ...string) {
	for _, v := range values {
		if v != "" {
			c.asked = true
		} else if m := emptyMessage(name); m != "" {
			c.empty[m] = struct{}{}
		}
	}
}

// refuseEncryption answers a write's encryption headers, for the writes
// radosgw's get_encryption_defaults does not precede: askedRefusal's, then
// emptyRefusal's. A bucket's default encryption is the op's to refuse, once
// the requester is authorized.
func refuseEncryption(r *op.Request, query bool) error {
	c := cryptRequestOf(r, query)
	if err := c.askedRefusal(); err != nil {
		return err
	}
	return c.emptyRefusal()
}

// askedRefusal answers NotImplemented for a write that asks for encryption:
// one carrying a value in an X-Amz- header that init_meta_info files in
// crypt_attribute_map, X-Amz-Server-Side-Encryption among them; in one
// naming a copy source's SSE-C key; and, when query is set, in a query
// parameter map_qs_metadata files there for a PutObject. radosgw encrypts
// SSE-C and fails SSE-S3 and SSE-KMS without a key server; rgw-go encrypts
// nothing in phase 1, and must not store in the clear what a client asked to
// have encrypted (docs/exclusions.md). A value anywhere wins over an empty
// instance of the same or another name (emptyRefusal).
func (c cryptRequest) askedRefusal() error {
	if c.asked {
		return fmt.Errorf("%w: server-side encryption is not served yet", op.ErrNotImplemented)
	}
	return nil
}

// The messages emptyRefusal answers with. sseEmptyMessage is v20.2.5's for
// an x-amz-server-side-encryption that is neither aws:kms nor AES256, which
// an empty one is
// (https://github.com/ceph/ceph/blob/v20.2.5/src/rgw/rgw_crypt.cc#L2280-L2285 at v20.2.5),
// and kmsEmptyMessage v20.2.5's for an empty KMS key id
// (https://github.com/ceph/ceph/blob/v20.2.5/src/rgw/rgw_crypt.cc#L2200-L2204 at v20.2.5);
// sseCEmptyMessage is rgw-go's own, as v20.2.5 takes an empty SSE-C name for
// none.
const (
	sseEmptyMessage  = "Server Side Encryption with KMS managed key requires HTTP header x-amz-server-side-encryption : aws:kms or AES256"
	kmsEmptyMessage  = "Server Side Encryption with KMS managed key requires HTTP header x-amz-server-side-encryption-aws-kms-key-id"
	sseCEmptyMessage = "Requests specifying Server Side Encryption with Customer provided keys must not send a server-side-encryption-customer header with no value."
)

// emptyMessage is the message emptyRefusal answers an empty value of the
// encryption name with, and "" for a name whose empty value is accepted. This
// is the rule, stated once: an empty value is refused for an SSE-C name, any
// name starting x-amz-server-side-encryption-customer-, and for a name that
// sets a policy condition key, since a name sent empty adds its key to the
// environment a bucket policy's Null condition reads, and so passes a Deny
// written to require it (rgw_iam_add_crypt_attrs,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L760-L774 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L790-L810 at v20.2.4):
// x-amz-server-side-encryption and x-amz-server-side-encryption-aws-kms-key-id.
// The context and the bucket key set no condition key and stay accepted, as
// does any name only the first 20 bytes of the prefix match.
func emptyMessage(name string) string {
	switch {
	case name == sseName:
		return sseEmptyMessage
	case name == sseKMSKeyIDName:
		return kmsEmptyMessage
	case strings.HasPrefix(name, sseCNamePrefix):
		return sseCEmptyMessage
	}
	return ""
}

// emptyRefusal answers InvalidArgument for a write that sent a name
// emptyMessage covers with no value, which askedRefusal has not already
// refused for a value. radosgw at v19.2.7 and v20.2.4 takes an empty one for
// none and stores the object unencrypted (rgw_s3_prepare_encrypt skips its
// SSE-C branch for an empty algorithm and ignores an empty
// x-amz-server-side-encryption,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_crypt.cc#L1040-L1048 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_crypt.cc#L1148-L1150 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1055-L1063 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1163-L1165 at v20.2.4),
// which a Null condition on the key the name adds counts as encrypted.
// v20.2.5 refuses an empty x-amz-server-side-encryption
// (https://github.com/ceph/ceph/blob/v20.2.5/src/rgw/rgw_crypt.cc#L2182-L2184 at v20.2.5),
// and rgw-go refuses it on both releases, and the rest of what emptyMessage
// covers too, an empty SSE-C name among them, which v20.2.5 does not
// (docs/differences.md). get_encryption_defaults refuses an empty KMS key
// id without x-amz-server-side-encryption by its name alone first, so this
// is the only refusal of it for a CopyObject, which does not run it.
func (c cryptRequest) emptyRefusal() error {
	for _, m := range []string{sseEmptyMessage, kmsEmptyMessage, sseCEmptyMessage} {
		if _, ok := c.empty[m]; ok {
			return op.ErrInvalidArgument.WithMessage(m)
		}
	}
	return nil
}

// The names get_encryption_defaults tells apart. sseName is the whole name
// x-amz-server-side-encryption, which is the KMS group's prefix there and
// cryptQueryPrefix, where init_meta_info files a header by its first 20 bytes
// (cryptPrefix).
const (
	sseName          = cryptQueryPrefix
	sseCNamePrefix   = sseName + "-customer-"
	sseKMSKeyIDName  = sseName + "-aws-kms-key-id"
	sseBucketKeyName = sseName + "-bucket-key-enabled"
	sseContextName   = sseName + "-context"
)

// encryptionDefaultsError is get_encryption_defaults' refusal of a request
// whose encryption names cannot go together
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L154-L272 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L151-L269 at v20.2.4),
// which it makes by the names alone, whatever their values: an SSE-C name and
// another is 400 InvalidArgument; without an SSE-C name,
// x-amz-server-side-encryption-aws-kms-key-id, -bucket-key-enabled or
// -context without x-amz-server-side-encryption is too, the first in that
// order. It runs for PutObject, UploadPart and UploadPartCopy,
// CreateMultipartUpload and POST, not for CopyObject, and before anything that
// reads a value, so a request it refuses is never 501.
func encryptionDefaultsError(r *op.Request, query bool) error {
	return cryptRequestOf(r, query).defaultsError()
}

// defaultsError is encryptionDefaultsError's answer for c.
func (c cryptRequest) defaultsError() error {
	var sseC, kms bool
	for name := range c.names {
		switch {
		case strings.HasPrefix(name, sseCNamePrefix):
			sseC = true
		case strings.HasPrefix(name, sseName):
			kms = true
		}
	}
	switch {
	case sseC && kms:
		return op.ErrInvalidArgument.WithMessage("Server side error - can't do sse-c & sse-kms|sse-s3")
	case sseC:
		return nil
	}
	if _, ok := c.names[sseName]; ok {
		return nil
	}
	for _, name := range []string{sseKMSKeyIDName, sseBucketKeyName, sseContextName} {
		if _, ok := c.names[name]; ok {
			return op.ErrInvalidArgument.WithMessage("incomplete rest sse parms: " + name + " not valid without kms")
		}
	}
	return nil
}

// readEncryption is what get_params makes of a write's encryption headers
// and parameters up to the refusal of an empty one: get_encryption_defaults'
// refusal of names that do not go together, which comes first, then the
// refusal of a request that asks for encryption. It returns the request, so
// that a caller that cannot refuse an empty header yet (UploadPart, which
// must read its upload first) asks it for emptyRefusal.
func readEncryption(r *op.Request, query bool) (cryptRequest, error) {
	c := cryptRequestOf(r, query)
	if err := c.defaultsError(); err != nil {
		return c, err
	}
	return c, c.askedRefusal()
}

// checkEncryption is readEncryption and then emptyRefusal, for the writes
// that decide the request alone.
func checkEncryption(r *op.Request, query bool) error {
	c, err := readEncryption(r, query)
	if err != nil {
		return err
	}
	return c.emptyRefusal()
}

// objectLock is PutObject's get_params refusal of a retention or legal hold
// for a bucket without object lock, InvalidRequest
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2669-L2673,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2830-L2834),
// which a copy makes too, where radosgw stores them on such a bucket
// (docs/ceph-upstream-bugs.md, "radosgw's CopyObject stores an object lock
// on a bucket without object lock"; docs/differences.md), and which
// CreateMultipartUpload's get_params makes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4051-L4054 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4502-L4505 at v20.2.4)
// and UploadPart's, which is PutObject's own. The callers make it once the
// requester is authorized, so that a refused requester learns nothing of the
// bucket's configuration.
func objectLock(r *op.Request) error {
	if namesObjectLock(r) && r.BucketRec.Info.Flags&meta.BucketObjLockEnabled == 0 {
		return op.ErrInvalidRequest
	}
	return nil
}

// namesObjectLock reports whether the request names a retention mode or a
// legal hold, which get_params keeps as obj_retention or obj_legal_hold.
func namesObjectLock(r *op.Request) bool {
	return hasHeader(r, "X-Amz-Object-Lock-Mode") || hasHeader(r, "X-Amz-Object-Lock-Legal-Hold")
}

// cephUnarmor is ceph_unarmor
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/armor.c#L99-L131 at v19.2.6
// and v20.2.4) into the 17-byte buffer RGWPutObj::execute passes: groups of
// four base64 digits, "-" and "_" read as "+" and "/" and "=" as a zero
// digit, line feeds between groups skipped; an "=" in a group's third or
// fourth place ends the decode there, whatever follows. A group cut short, a
// byte that is no digit and output past the buffer fail.
func cephUnarmor(src string) ([]byte, bool) {
	const capacity = md5.Size + 1
	dst := make([]byte, 0, capacity)
	put := func(c int) bool {
		if len(dst) == capacity {
			return false
		}
		dst = append(dst, byte(c)) //nolint:gosec // the low byte, as SET_DST stores a char
		return true
	}
	for i := 0; i < len(src); {
		if src[i] == '\n' {
			i++
			continue
		}
		if i+4 > len(src) {
			return nil, false
		}
		a, b, c, d := armorBits(src[i]), armorBits(src[i+1]), armorBits(src[i+2]), armorBits(src[i+3])
		if a < 0 || b < 0 || c < 0 || d < 0 {
			return nil, false
		}
		if !put(a<<2 | b>>4) {
			return nil, false
		}
		if src[i+2] == '=' {
			return dst, true
		}
		if !put((b&15)<<4 | c>>2) {
			return nil, false
		}
		if src[i+3] == '=' {
			return dst, true
		}
		if !put((c&3)<<6 | d) {
			return nil, false
		}
		i += 4
	}
	return dst, true
}

// armorBits is armor.c's decode_bits: a base64 digit's value, -1 for a byte
// that is none.
func armorBits(c byte) int {
	switch {
	case 'A' <= c && c <= 'Z':
		return int(c - 'A')
	case 'a' <= c && c <= 'z':
		return int(c-'a') + 26
	case '0' <= c && c <= '9':
		return int(c-'0') + 52
	case c == '+' || c == '-':
		return 62
	case c == '/' || c == '_':
		return 63
	case c == '=':
		return 0
	}
	return -1
}

// successStatus is get_success_retcode over rgw_s3_success_create_obj_status
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2719-L2728
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2736-L2740,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2880-L2889
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2897-L2901):
// 201 or 204 when the option names it, otherwise 200.
func successStatus(r *op.Request) int {
	if r.Env.Conf != nil {
		if v, err := r.Env.Conf.Int64("rgw_s3_success_create_obj_status"); err == nil && (v == http.StatusCreated || v == http.StatusNoContent) {
			return int(v)
		}
	}
	return http.StatusOK
}

// setSystemMtime is send_response's Rgwx-Mtime for a system request
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2779-L2781,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2949-L2951;
// dump_epoch_header,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L487-L496 at v19.2.6):
// the mtime's seconds and nanoseconds.
func setSystemMtime(h http.Header, r *op.Request, mtime time.Time) {
	if r.Identity.System && !mtime.IsZero() {
		h.Set("Rgwx-Mtime", fmt.Sprintf("%d.%09d", mtime.Unix(), mtime.Nanosecond()))
	}
}

// hasHeader reports whether the request carries the header, empty or not.
func hasHeader(r *op.Request, name string) bool {
	_, ok := op.HeaderValue(r.Header, name)
	return ok
}

// headerPtr is a header's last value, nil when it is absent: a header
// present with an empty value is a condition radosgw checks.
func headerPtr(r *op.Request, name string) *string {
	if v, ok := op.HeaderValue(r.Header, name); ok {
		return &v
	}
	return nil
}
