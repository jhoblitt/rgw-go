package op

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// defaultPartUploadLimit is rgw_multipart_part_upload_limit's default
// (rgw.yaml.in at v19.2.6 and v20.2.4), for a configuration that cannot be
// read.
const defaultPartUploadLimit = 10000

// CompleteMultipart is RGWCompleteMultipart, CompleteMultipartUpload
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6345-L6541 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7021-L7431 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1580-L1595 at v19.2.6;
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4064-L4108 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4565-L4644 at v20.2.4).
// Execute reads the completion document from r.Body, through the
// authenticator's verifying reader to its final Read, before it parses it,
// and completes the upload through MultipartStore.Complete.
type CompleteMultipart struct {
	UploadID string
	// IfMatch and IfNoneMatch are the headers' values, nil when absent: a
	// header present with an empty value is a condition, as v20.2.4 takes it
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4572-L4573 at v20.2.4).
	// rgw-go applies them on both releases (Upload.IfMatch).
	IfMatch, IfNoneMatch *string
	// ChecksumField is the request's checksum fields, which
	// SuppliedForComplete reads (ChecksumInput).
	ChecksumField func(name string) (string, bool)
	// SSECAlgorithm is the x-amz-server-side-encryption-customer-algorithm
	// header, nil when absent: beast files a header sent empty, so
	// rgw_s3_prepare_decrypt refuses it as no AES256 rather than as missing
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1340-L1354 at v20.2.4).
	SSECAlgorithm *string
	// SSECKey and SSECKeyMD5 are the -key and -key-MD5 headers, "" when
	// absent, with which a completion of an SSE-C upload names the upload's
	// key (verifyEncryption).
	SSECKey, SSECKeyMD5 string
	// SSECSent reports whether the request sends any header that beast
	// files under a name starting HTTP_X_AMZ_SERVER_SIDE_ENCRYPTION_CUSTOMER_,
	// as v20.2.5's exists_prefix asks
	// (https://github.com/ceph/ceph/blob/v20.2.5/src/rgw/rgw_rest_s3.cc#L4627-L4629 at v20.2.5).
	SSECSent bool
	// Secure is rgw_transport_is_secure: TLS, or a forwarded protocol radosgw
	// trusts.
	Secure bool
	// AskedEncryption is the protocol's refusal of a request that asks for
	// encryption (askedRefusal in internal/s3), nil when it asks for none.
	// rgw-go encrypts nothing and begins no SSE-C upload, so it stands ahead
	// of everything Execute reads, an SSE-C upload's key included.
	AskedEncryption error
	// EmptyEncryption is the protocol's refusal of a request that sent an
	// encryption header it refuses with no value (emptyMessage in
	// internal/s3), nil when it sent none. It stands behind AskedEncryption,
	// as a value wins over an empty instance, and behind verifyEncryption,
	// which an SSE-C upload's headers answer first.
	EmptyEncryption error

	// Parts is the document's parts as ParseCompleteMultipart reads them.
	Parts []CompletePart
	ETag  string
	Size  uint64
	Mtime time.Time
	// Tags and OrigSize are the completed object's tags attr and its size
	// before compression, as the store reports them (PutResult).
	Tags     []byte
	OrigSize uint64
	// VersionID is the id of the version the store completed
	// (ResponseVersionID), "" for none: for a completion it finishes rather
	// than writes, the version that earlier completion wrote.
	VersionID string
	// Checksum is the combined checksum the store stored, nil for none.
	Checksum *cksum.Cksum
	// Echo is what send_response echoes of the encryption verifyEncryption
	// decided, set once the decision passes, so that an error after it
	// echoes it too, as radosgw's does
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4604-L4612 at v20.2.4).
	Echo CryptEcho
}

// CryptEcho is a Tentacle completion's crypt_http_responses, which
// rgw_s3_prepare_decrypt gathers
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1415-L1418,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1450-L1453 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1518-L1520 at v20.2.4).
// A Squid radosgw's completion echoes none
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4106-L4112 at v19.2.7),
// so a Squid completion's is zero.
type CryptEcho struct {
	// SSECKeyMD5 is the key MD5 header as the request sent it, once it named
	// an SSE-C upload's key, whose algorithm, AES256, is echoed beside it; ""
	// for none.
	SSECKeyMD5 string
	// SSE is x-amz-server-side-encryption: aws:kms for SSE-KMS, AES256 for
	// SSE-S3, "" for none.
	SSE string
	// KMSKeyID is an SSE-KMS upload's user.rgw.crypt.keyid as stored.
	KMSKeyID string
}

var _ Op = (*CompleteMultipart)(nil)

// Name is RGWCompleteMultipart::name.
func (o *CompleteMultipart) Name() string { return "complete_multipart" }

// Action is the action RGWCompleteMultipart::verify_permission authorizes.
func (o *CompleteMultipart) Action() policy.Action { return policy.S3PutObject }

// OpMask is RGW_OP_TYPE_WRITE.
func (o *CompleteMultipart) OpMask() uint32 { return OpTypeWrite }

// Init is init_permissions' load of the bucket, a missing one NoSuchBucket.
func (o *CompleteMultipart) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWCompleteMultipart::verify_permission
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6345-L6360 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7021-L7036 at v20.2.4).
func (o *CompleteMultipart) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyBucketPermission(ctx, r, policy.S3PutObject, acl.PermFor(policy.S3PutObject))
}

// Execute is RGWCompleteMultipart::execute in its order
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6367-L6416 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7163-L7211 at v20.2.4):
// get_params, whose empty upload id is -ENOTSUP, 500 UnknownError; a request
// that asks for encryption refused (AskedEncryption) before its body is read,
// so nothing of the upload is read, assembled or written; the body read as
// ReadParamBody reads it under rgw_max_put_param_size; an
// empty body, a document that does not parse or holds no part, MalformedXML;
// more parts than rgw_multipart_part_upload_limit once repeats collapse,
// InvalidRange (-ERANGE); then, where execute reads the upload's info
// (readCompleteUpload), the upload's encryption decided where v20.2.5
// decides it (verifyEncryption), which v20.2.4 does in get_params, before
// the document parses, an encryption header sent empty refused
// (EmptyEncryption) for an upload that is found, and the checksum the
// request supplies (completeChecksum); then the completion, whose head takes the request id
// as its tag, at VersionForWrite's instance. An answer the store gives from
// an object a completion already wrote, which a retry of a completion that
// succeeded and an upload id that names no upload both meet, is decided as
// an upload is, over that head's crypt attrs (completedHead), before it is
// answered; radosgw answers it from check_previously_completed with no
// decision at all
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7248-L7256 at v20.2.4,
// https://github.com/ceph/ceph/blob/v20.2.5/src/rgw/rgw_op.cc#L7431-L7443 at v20.2.5),
// rendering the head's combined checksum (docs/ceph-upstream-bugs.md,
// "radosgw shows an encrypted upload's plaintext checksums to a requester
// without its key").
func (o *CompleteMultipart) Execute(ctx context.Context, r *Request) error {
	if o.UploadID == "" {
		return fmt.Errorf("%w: no upload id", ErrUnknown)
	}
	if o.AskedEncryption != nil {
		return o.AskedEncryption
	}
	body, err := ReadParamBody(r, confSize(r, "rgw_max_put_param_size", defaultMaxPutParamSize))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return fmt.Errorf("%w: an empty completion", ErrMalformedXML)
	}
	if o.Parts, err = ParseCompleteMultipart(body); err != nil {
		return err
	}
	if limit := partUploadLimit(r); int64(len(o.Parts)) > limit {
		return fmt.Errorf("%w: %d parts over rgw_multipart_part_upload_limit %d", ErrInvalidRange, len(o.Parts), limit)
	}
	if aerr := aliasedUpload(o.UploadID); aerr != nil {
		return aerr
	}
	up, err := readCompleteUpload(ctx, r, o.UploadID)
	if err != nil {
		return err
	}
	if cerr := o.verifyEncryption(ctx, r, up, false); cerr != nil {
		return cerr
	}
	if up != nil && o.EmptyEncryption != nil {
		o.Echo = CryptEcho{}
		return o.EmptyEncryption
	}
	supplied, err := completeChecksum(r, up, ChecksumInput{Field: o.ChecksumField})
	if err != nil {
		return err
	}
	res, err := r.Env.Multipart.Complete(ctx, &Upload{
		ID: o.UploadID, Bucket: r.BucketRec, Key: writeKey(r), WriteTag: r.ID,
		IfMatch: storeCondition(o.IfMatch), IfNoneMatch: storeCondition(o.IfNoneMatch), SuppliedChecksum: supplied,
	}, o.Parts)
	if err != nil {
		return err
	}
	if res.Crypt != nil {
		if cerr := o.verifyEncryption(ctx, r, completedHead(o.UploadID, res), true); cerr != nil {
			return cerr
		}
	}
	o.ETag, o.Size, o.Mtime, o.Checksum = res.ETag, res.Size, res.Mtime, res.Checksum
	o.Tags, o.OrigSize = res.Tags, res.OrigSize
	o.VersionID = ResponseVersionID(r.BucketRec, res.Version)
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *CompleteMultipart) Complete(context.Context, *Request) {}

// readCompleteUpload is execute's read of upload id's info, nil for an
// upload GetUpload does not find, which MultipartStore.Complete answers, as
// it answers a completion retried after it succeeded.
func readCompleteUpload(ctx context.Context, r *Request, id string) (*Upload, error) {
	up, err := r.Env.Multipart.GetUpload(ctx, r.BucketRec, r.Object, id)
	if errors.Is(err, ErrNoSuchUpload) {
		return nil, nil
	}
	return up, err
}

// verifyEncryption decides a completion of up, the upload as read, nil for
// one GetUpload did not find, by its crypt mode, setting Echo when it
// passes. A completion reads no part's data and writes the meta object's
// attrs whatever the mode, so it needs a key only to answer as Amazon S3
// does:
//   - an upload of a mode rgw-go does not know is refused
//     (refuseUnknownCryptMode);
//   - an SSE-C upload is decided as v20.2.5's verify_encryption, Amazon
//     S3's rule, decides it, on both releases (verifySSEC)
//     (https://github.com/ceph/ceph/blob/v20.2.5/src/rgw/rgw_rest_s3.cc#L4622-L4635 at v20.2.5);
//   - an SSE-S3 upload completes, as Amazon S3 completes one, which a
//     Tentacle radosgw refuses only for want of the key whose header it
//     echoes
//     (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1495-L1523 at v20.2.4);
//   - an SSE-KMS upload without a checksum type completes once
//     rgw_crypt_require_ssl takes the transport, as radosgw checks it before
//     any key
//     (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1423-L1428 at v20.2.4);
//     one with a checksum type, which Amazon S3 completes only for a
//     requester allowed kms:Decrypt, and an RGW-AUTO upload, which Amazon S3
//     has no counterpart of, are refused as a radosgw without a key server
//     refuses them (storedCryptRefusal).
//
// On Squid every SSE-KMS, SSE-S3 and RGW-AUTO upload completes and nothing
// is echoed: a v19.2.7 radosgw's completion reads no crypt attr
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4094-L4112 at v19.2.7),
// and no Squid upload has a checksum type.
//
// completed says up stands for an object a completion already wrote
// (completedHead), whose answer echoes no SSE-C pair: no radosgw echoes one
// there, and Amazon S3's SDK reads neither header of a completion. The
// x-amz-server-side-encryption pair, which it reads, is echoed there too.
func (o *CompleteMultipart) verifyEncryption(ctx context.Context, r *Request, up *Upload, completed bool) error {
	if up == nil {
		return nil
	}
	tentacle := r.Env.Zone.Release() >= denc.Tentacle
	checksum := up.Checksum.Type != cksum.None
	mode := storedCryptMode(up.Attrs)
	switch {
	case mode == cryptNone:
		return nil
	case mode == cryptUnknown:
		return refuseUnknownCryptMode(up)
	case mode == cryptSSEC:
		return o.verifySSEC(r, up.Attrs[meta.AttrCryptKeyMD5], checksum, tentacle && !completed)
	case !tentacle:
		return nil
	case mode == cryptSSES3:
		o.Echo.SSE = "AES256"
		return nil
	case mode == cryptSSEKMS && !checksum:
		if err := requireSSL(r, o.Secure); err != nil {
			return err
		}
		o.Echo.SSE, o.Echo.KMSKeyID = "aws:kms", string(up.Attrs[meta.AttrCryptKeyID])
		return nil
	}
	return storedCryptRefusal(ctx, r, up.Attrs, o.sseCHeaders())
}

// verifySSEC is v20.2.5's verify_encryption for an SSE-C upload whose key
// MD5 stored holds: an upload without a checksum type completes without its
// key when the request sends no SSE-C header (SSECSent); otherwise
// rgw_crypt_require_ssl refuses an insecure request, and the headers must
// name the upload's key (sseCKeyError, which takes an algorithm header sent
// empty for no AES256), which is then echoed when echo says so. A v20.2.4
// radosgw asks for the key on every SSE-C completion
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4591-L4598 at v20.2.4),
// and a v19.2.7 one on none (docs/differences.md).
func (o *CompleteMultipart) verifySSEC(r *Request, stored []byte, checksum, echo bool) error {
	if !checksum && !o.SSECSent {
		return nil
	}
	if err := requireSSL(r, o.Secure); err != nil {
		return err
	}
	if err := sseCKeyError(stored, o.sseCHeaders()); err != nil {
		return err
	}
	if echo {
		o.Echo.SSECKeyMD5 = o.SSECKeyMD5
	}
	return nil
}

// sseCHeaders is the request's SSE-C headers and transport as
// rgw_s3_prepare_decrypt reads them.
func (o *CompleteMultipart) sseCHeaders() sseCRequest {
	return sseCRequest{algorithm: o.SSECAlgorithm, key: o.SSECKey, keyMD5: o.SSECKeyMD5, secure: o.Secure}
}

// completedHead is the object a completion of upload id found already
// written, as res describes it, standing for the upload verifyEncryption
// decides: its crypt attrs, and a checksum type when it stores a combined
// checksum, which the completion's answer would render.
func completedHead(id string, res *PutResult) *Upload {
	head := &Upload{ID: id, Attrs: res.Crypt}
	if res.Checksum != nil {
		head.Checksum.Type = res.Checksum.Type
	}
	return head
}

// CryptAttrs is attrs' user.rgw.crypt.* entries, copied, nil when it has
// none: what a completion decides of a head a completion already wrote
// (PutResult.Crypt).
func CryptAttrs(attrs map[string][]byte) map[string][]byte {
	var out map[string][]byte
	for k, v := range attrs {
		if strings.HasPrefix(k, meta.AttrCryptPrefix) {
			if out == nil {
				out = map[string][]byte{}
			}
			out[k] = bytes.Clone(v)
		}
	}
	return out
}

// refuseUnknownCryptMode refuses with NotImplemented the completion of an
// upload whose crypt mode is not empty and is one rgw-go does not know, as
// v20.2.5's GCM modes are, before anything is written. A v20.2.5 radosgw's
// completion of such an upload writes the plaintext size and each part's
// number and salt
// (https://github.com/ceph/ceph/blob/v20.2.5/src/rgw/driver/rados/rgw_sal_rados.cc#L4486-L4500 at v20.2.5),
// which its read of the object needs; rgw-go's writes neither, so no gateway
// could return the data of the object it completed. A v19.2.7 or v20.2.4
// radosgw, which takes the mode for none, completes it
// (docs/differences.md).
func refuseUnknownCryptMode(up *Upload) error {
	if up == nil || storedCryptMode(up.Attrs) != cryptUnknown {
		return nil
	}
	return fmt.Errorf("%w: upload %s has crypt mode %q, which rgw-go does not know", ErrNotImplemented, up.ID, up.Attrs[meta.AttrCryptMode])
}

// partUploadLimit is rgw_multipart_part_upload_limit, its default when the
// configuration cannot be read.
func partUploadLimit(r *Request) int64 {
	if r.Env.Conf != nil {
		if v, err := r.Env.Conf.Int64("rgw_multipart_part_upload_limit"); err == nil {
			return v
		}
	}
	return defaultPartUploadLimit
}
