package driver

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// hiddenPools are the zone's pools whose objects errors and logs name by
// the kind of object they hold: the access key, email and Swift key
// indexes and rgw-go's key holder index, whose objects are named by a
// credential or an email, and rgw-go's bucket claims. An account's email
// redirect is an object of the email index. Errors and logs name an object
// of one by its pool and kind alone.
type hiddenPools map[meta.Pool]string

func newHiddenPools(p meta.ZoneParams) hiddenPools {
	return hiddenPools{
		p.UserKeysPool:                radosclient.KindUserKeyIndex,
		p.UserEmailPool:               radosclient.KindUserEmailIndex,
		p.UserSwiftPool:               radosclient.KindUserSwiftIndex,
		keyHolderPool(p.UserKeysPool): radosclient.KindUserKeyHolderIndex,
		bucketClaimPool(p.DomainRoot): radosclient.KindBucketClaim,
	}
}

// name names the object oid in pool for errors: "<pool>/<oid>", or
// "<pool> (<kind>)" for an object of a hidden pool.
func (h hiddenPools) name(pool meta.Pool, oid string) string {
	if kind, ok := h[pool]; ok {
		return pool.String() + " (" + kind + ")"
	}
	return pool.String() + "/" + oid
}

// attr is the log attribute naming the object oid in pool: its oid, or the
// kind of an object of a hidden pool.
func (h hiddenPools) attr(pool meta.Pool, oid string) slog.Attr {
	if kind, ok := h[pool]; ok {
		return slog.String("kind", kind)
	}
	return slog.String("oid", oid)
}

// hide returns err from an operation on an object of pool, its text
// replaced when the pool is hidden: the seam names the object an operation
// failed on, and a seam that does not know the zone's layout names it by its
// id. errors.Is and errors.As still reach err.
func (h hiddenPools) hide(pool meta.Pool, err error) error {
	if _, ok := h[pool]; !ok || err == nil {
		return err
	}
	return &hiddenErr{err: err}
}

// hiddenErr is an error whose text names no object: the errno or context
// error it carries, or that it failed.
type hiddenErr struct{ err error }

func (e *hiddenErr) Error() string {
	if re, ok := errors.AsType[*radosclient.Error](e.err); ok {
		return (&radosclient.Error{Errno: re.Errno}).Error()
	}
	for _, known := range []error{
		context.Canceled, context.DeadlineExceeded, errNegativeEntry, radosclient.ErrClosed, radosclient.ErrIncomplete,
	} {
		if errors.Is(e.err, known) {
			return known.Error()
		}
	}
	return "rados: the operation failed"
}

func (e *hiddenErr) Unwrap() error { return e.err }
