package op

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // SSE-C names its key by the key's MD5
	"fmt"
	"log/slog"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// The messages rgw_s3_prepare_decrypt sets for an SSE-C request it refuses.
const (
	sseCAlgorithmMissing = "Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm."
	sseCAlgorithmInvalid = "The requested encryption algorithm is not valid, must be AES256."
	sseCKeyInvalid       = "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key."
	sseCKeyMD5Invalid    = "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key md5."
	sseCKeyMismatch      = "The calculated MD5 hash of the key did not match the hash that was provided."
)

// sseCKeySize is AES_256_CBC::AES_256_KEYSIZE.
const sseCKeySize = 32

// sseCRequest is what rgw_s3_prepare_decrypt reads of a request: its
// x-amz-server-side-encryption-customer-algorithm, -key and -key-MD5
// headers, "" when absent, and whether rgw_transport_is_secure takes it.
type sseCRequest struct {
	algorithm, key, keyMD5 string
	secure                 bool
}

// cryptMode is a stored crypt mode as rgw-go reads it: every caller that
// decides by a mode classifies it here, so one that rgw-go does not know is
// cryptUnknown everywhere, never taken for none.
type cryptMode int

const (
	// cryptNone is no mode attr, or an empty one, which radosgw takes for
	// none.
	cryptNone cryptMode = iota
	cryptSSEC
	cryptSSEKMS
	cryptSSES3
	cryptRGWAuto
	// cryptUnknown is any other mode, such as a GCM mode v20.2.5 writes.
	cryptUnknown
)

// storedCryptMode classifies the crypt mode attrs store, by the names
// rgw_s3_prepare_encrypt writes
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1045-L1315 at v20.2.4).
func storedCryptMode(attrs map[string][]byte) cryptMode {
	switch string(attrs[meta.AttrCryptMode]) {
	case "":
		return cryptNone
	case "SSE-C-AES256":
		return cryptSSEC
	case "SSE-KMS":
		return cryptSSEKMS
	case "AES256":
		return cryptSSES3
	case "RGW-AUTO":
		return cryptRGWAuto
	}
	return cryptUnknown
}

// storedCryptRefusal is rgw_s3_prepare_decrypt past its refusal of
// x-amz-server-side-encryption on a read
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_crypt.cc#L1317-L1505 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1333-L1527 at v20.2.4):
// its answer to req for the crypt mode attrs store, an object's for GET and
// HEAD, an upload's for each of its parts, and a copy source's for
// UploadPartCopy. rgw-go has no key server, so
// SSE-KMS, SSE-S3 and RGW-AUTO answer as radosgw does without one
// (docs/exclusions.md), and it implements no SSE-C yet, so an SSE-C request
// that passes every check is NotImplemented. rgw_crypt_require_ssl, true
// when it cannot be read, refuses a request that is not secure, except for
// RGW-AUTO, and for SSE-S3 on Tentacle.
//
// It is nil only for attrs without a mode or with an empty one, which
// radosgw takes for none. A mode it does not name, one a newer radosgw
// writes, as v20.2.5's rgw_s3_prepare_decrypt reads four GCM
// modes
// (https://github.com/ceph/ceph/blob/v20.2.5/src/rgw/rgw_crypt.cc#L2486-L2978 at v20.2.5),
// is NotImplemented, where radosgw at both releases takes it for no
// encryption: rgw-go neither serves its stored bytes nor writes a part of
// it in the clear.
func storedCryptRefusal(ctx context.Context, r *Request, attrs map[string][]byte, req sseCRequest) error {
	insecure := confBool(r, "rgw_crypt_require_ssl", true) && !req.secure
	errInsecure := fmt.Errorf("%w: rgw_crypt_require_ssl refuses an insecure request", ErrInvalidRequest)
	switch storedCryptMode(attrs) {
	case cryptNone:
		return nil
	case cryptSSEC:
		if insecure {
			return errInsecure
		}
		return checkSSEC(ctx, r, attrs[meta.AttrCryptKeyMD5], req)
	case cryptSSEKMS:
		if insecure {
			return errInsecure
		}
		return ErrInvalidArgument.WithMessage("Failed to retrieve the actual key, kms-keyid: " + string(attrs[meta.AttrCryptKeyID]))
	case cryptRGWAuto:
		// rgw_crypt_default_encryption_key, unset, does not decode to 32
		// bytes: -EIO.
		return fmt.Errorf("%w: no default encryption key for %s", ErrUnknown, r.Object.Name)
	case cryptSSES3:
		if insecure && r.Env.Zone.Release() < denc.Tentacle {
			return errInsecure
		}
		return ErrInvalidArgument.WithMessage("Failed to retrieve the actual key")
	default:
		return fmt.Errorf("%w: crypt mode %q is not one rgw-go knows", ErrNotImplemented, attrs[meta.AttrCryptMode])
	}
}

// RefuseEncryptedSource is CopyObject's refusal of a source that carries a
// crypt mode attr, of any value, NotImplemented, as a Squid radosgw's
// copy_obj refuses one
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L4787-L4795 at v19.2.7).
// A Tentacle radosgw decrypts it, which rgw-go cannot do for a key-server
// mode and does not do for SSE-C yet; copying the source's stored bytes
// would make ciphertext the copy's data.
func RefuseEncryptedSource(src *ObjectState) error {
	if _, ok := src.Attrs[meta.AttrCryptMode]; !ok {
		return nil
	}
	return fmt.Errorf("%w: copy source %s is encrypted", ErrNotImplemented, src.Key.Name)
}

// RefuseEncryptedPartSource is a store's refusal to copy n bytes of a
// source whose crypt mode is not empty into a part, NotImplemented; a copy
// of no byte copies no ciphertext. UploadPartCopy decides such a source
// before it asks the store (UploadPart.refuseEncryptedSource), so this
// keeps a store from copying ciphertext as a part's data only for a caller
// that did not.
func RefuseEncryptedPartSource(src *ObjectState, n uint64) error {
	if n == 0 || storedCryptMode(src.Attrs) == cryptNone {
		return nil
	}
	return fmt.Errorf("%w: part copy source %s is encrypted", ErrNotImplemented, src.Key.Name)
}

// checkSSEC is rgw_s3_prepare_decrypt's SSE-C branch: the key checks
// (sseCKeyError), and NotImplemented for a key that passes them, which
// radosgw decrypts or encrypts with.
func checkSSEC(ctx context.Context, r *Request, stored []byte, req sseCRequest) error {
	if err := sseCKeyError(stored, req); err != nil {
		return err
	}
	slog.InfoContext(ctx, "refusing sse-c: customer keys are not implemented",
		slog.String("bucket", r.Bucket), slog.String("key", r.Object.Name))
	return fmt.Errorf("%w: SSE-C is not implemented", ErrNotImplemented)
}

// sseCKeyError is rgw_s3_prepare_decrypt's checks of req's SSE-C headers: the
// algorithm, the key and its MD5, which must match both the key and stored,
// the MD5 the object or upload keeps. It is nil for headers that name the
// stored key.
func sseCKeyError(stored []byte, req sseCRequest) error {
	switch req.algorithm {
	case "":
		return ErrInvalidArgument.WithMessage(sseCAlgorithmMissing)
	case "AES256":
	default:
		return ErrInvalidEncryptionAlgorithm.WithMessage(sseCAlgorithmInvalid)
	}
	key, ok := fromBase64(req.key)
	if !ok || len(key) != sseCKeySize {
		return ErrInvalidArgument.WithMessage(sseCKeyInvalid)
	}
	keyMD5, ok := fromBase64(req.keyMD5)
	if !ok || len(keyMD5) != md5.Size {
		return ErrInvalidArgument.WithMessage(sseCKeyMD5Invalid)
	}
	sum := md5.Sum(key) //nolint:gosec // SSE-C names its key by the key's MD5
	if !bytes.Equal(sum[:], keyMD5) || !bytes.Equal(stored, keyMD5) {
		return ErrInvalidArgument.WithMessage(sseCKeyMismatch)
	}
	return nil
}
