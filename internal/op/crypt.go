package op

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // SSE-C names its key by the key's MD5
	"errors"
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

// errUnknownCryptMode marks storedCryptRefusal's refusal of a crypt mode it
// does not name.
var errUnknownCryptMode = errors.New("a crypt mode rgw-go does not know")

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
// It is nil only for attrs without a mode. A mode it does not name, one a
// newer radosgw writes, as v20.2.5's rgw_s3_prepare_decrypt reads four GCM
// modes
// (https://github.com/ceph/ceph/blob/v20.2.5/src/rgw/rgw_crypt.cc#L2486-L2978 at v20.2.5),
// is NotImplemented, matching errUnknownCryptMode too, where radosgw at
// both releases takes it for no encryption.
func storedCryptRefusal(ctx context.Context, r *Request, attrs map[string][]byte, req sseCRequest) error {
	insecure := confBool(r, "rgw_crypt_require_ssl", true) && !req.secure
	errInsecure := fmt.Errorf("%w: rgw_crypt_require_ssl refuses an insecure request", ErrInvalidRequest)
	switch mode := string(attrs[meta.AttrCryptMode]); mode {
	case "":
		return nil
	case "SSE-C-AES256":
		if insecure {
			return errInsecure
		}
		return checkSSEC(ctx, r, attrs[meta.AttrCryptKeyMD5], req)
	case "SSE-KMS":
		if insecure {
			return errInsecure
		}
		return ErrInvalidArgument.WithMessage("Failed to retrieve the actual key, kms-keyid: " + string(attrs[meta.AttrCryptKeyID]))
	case "RGW-AUTO":
		// rgw_crypt_default_encryption_key, unset, does not decode to 32
		// bytes: -EIO.
		return fmt.Errorf("%w: no default encryption key for %s", ErrUnknown, r.Object.Name)
	case "AES256":
		if insecure && r.Env.Zone.Release() < denc.Tentacle {
			return errInsecure
		}
		return ErrInvalidArgument.WithMessage("Failed to retrieve the actual key")
	default:
		return fmt.Errorf("%w: %w %q", ErrNotImplemented, errUnknownCryptMode, mode)
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
	if n == 0 || len(src.Attrs[meta.AttrCryptMode]) == 0 {
		return nil
	}
	return fmt.Errorf("%w: part copy source %s is encrypted", ErrNotImplemented, src.Key.Name)
}

// checkSSEC is rgw_s3_prepare_decrypt's SSE-C branch: the algorithm, the key
// and its MD5, which must match both the key and stored, the MD5 the object
// or upload keeps.
func checkSSEC(ctx context.Context, r *Request, stored []byte, req sseCRequest) error {
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
	slog.InfoContext(ctx, "refusing sse-c: customer keys are not implemented",
		slog.String("bucket", r.Bucket), slog.String("key", r.Object.Name))
	return fmt.Errorf("%w: SSE-C is not implemented", ErrNotImplemented)
}
