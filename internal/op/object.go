package op

import (
	"context"
	"io"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// ObjectState is what one head read tells about an object.
type ObjectState struct {
	Bucket *BucketRecord
	Key    meta.ObjKey
	Exists bool
	Size   uint64
	Mtime  time.Time
	// Epoch is the version the head read returned.
	Epoch uint64
	Attrs map[string][]byte
	// Manifest is nil for an object held entirely in its head.
	Manifest    *meta.Manifest
	Compression *meta.CompressionInfo
	ETag        string
	// WriteTag is user.rgw.idtag.
	WriteTag     string
	StorageClass string
	ContentType  string
	// Head is the first bytes of the head object, rgw_max_chunk_size or the
	// whole head when shorter, filled by PrefetchObject; nil after StatObject.
	Head []byte
	// Version is the head's cls_version, read in the same op as the stat, as
	// RGWObjVersionTracker::prepare_op_for_read composes cls_version_read
	// into every raw_obj_stat (rgw_rados.cc:158-167 and :8843-8845 at v19.2.6,
	// :166-175 and :9787-9789 at v20.2.4); zero for an object that carries
	// none.
	Version meta.ObjVersion

	// defaultACLLogged is set once ObjectACLFor has warned that the head
	// carries no ACL: radosgw reads the ACL once per request and warns once,
	// where rgw-go's authorizer and an op may each derive it from the state.
	defaultACLLogged bool
}

// ByteRange is a resolved range: Length bytes from Offset.
type ByteRange struct {
	Offset, Length uint64
}

// PutParams shapes a write.
type PutParams struct {
	// Attrs are the head xattrs to write: ACL, content type, user metadata.
	Attrs map[string][]byte
	// Size is the declared body size, -1 when unknown.
	Size int64
	// Mtime is the object mtime; zero means now.
	Mtime        time.Time
	StorageClass string
	// IfMatch and IfNoneMatch are ETags; "*" matches any object. "" is
	// unset, and a header present with an empty value arrives as a single
	// NUL, which matches no ETag, as radosgw's compare of the empty string
	// with the ETag matches none (storeCondition).
	IfMatch, IfNoneMatch string
	// ETag, when set, is stored instead of the computed MD5 (multipart completion).
	ETag string
	// Tag is the write tag: radosgw uses the request id (rgw_op.cc:4288 and
	// driver/rados/rgw_putobj_processor.cc:377 at v19.2.6, :4501 and :407 at
	// v20.2.4), so user.rgw.idtag and user.rgw.tail_tag hold it
	// NUL-terminated and the index entry holds it bare. Empty selects
	// append_rand_alpha's form, "_" and 31 random characters.
	Tag string
	// ContentMD5 is the decoded Content-MD5 header, 16 bytes, nil when the
	// request carried none; a body whose MD5 differs is ErrBadDigest before
	// the head is written (RGWPutObj::execute, rgw_op.cc:4483-4486 at
	// v19.2.6, :4715-4718 at v20.2.4).
	ContentMD5 []byte
}

// PutResult describes the object a write produced.
type PutResult struct {
	ETag    string
	Size    uint64
	Mtime   time.Time
	Epoch   uint64
	Version string
}

// DeleteParams shapes a delete.
type DeleteParams struct {
	// IfMatch is If-Match: "*" or an ETag the object's must begin with,
	// unquoted first (check_preconditions, driver/rados/rgw_rados.cc:7287-7306
	// at v20.2.4); empty means unset, and a header or ETag element present
	// with an empty value arrives as a single NUL, which matches no ETag
	// (storeCondition).
	IfMatch string
	// Mtime is the delete time; zero means now.
	Mtime time.Time
	// UnmodifiedSince is x-amz-delete-if-unmodified-since: the object must not
	// have changed after it, compared in whole seconds; zero means unset
	// (Delete::delete_obj, driver/rados/rgw_rados.cc:5860-5875 at v19.2.6,
	// :6609-6624 at v20.2.4).
	UnmodifiedSince time.Time
	// IfMatchSize is x-amz-if-match-size: the object's size must equal it;
	// nil means unset (check_preconditions, :7264-7269 at v20.2.4).
	IfMatchSize *uint64
	// IfMatchLastModified is x-amz-if-match-last-modified-time: the object's
	// mtime must equal it, compared in whole seconds; zero means unset
	// (check_preconditions, :7271-7284 at v20.2.4).
	IfMatchLastModified time.Time
}

// CopyParams shapes a copy.
type CopyParams struct {
	// Attrs replace the destination's xattrs when ReplaceAttrs, else they are
	// merged over the source's.
	Attrs        map[string][]byte
	ReplaceAttrs bool
	Mtime        time.Time
	StorageClass string
	// IfMatch and IfNoneMatch are the x-amz-copy-source-if-match and
	// -if-none-match conditions, which copy_obj applies to the source's ETag.
	// The op checks them against the source state before CopyObject; the
	// RADOS driver does not check them again, while memstore does. "" is
	// unset, and a header present with an empty value arrives as a single
	// NUL (storeCondition).
	IfMatch, IfNoneMatch string
	// Tag is the write tag: radosgw passes the request id (rgw_op.cc:5672 at
	// v19.2.6, :6246 at v20.2.4), under which a copy that shares the source's
	// tails takes their references and writes its head. Empty selects
	// append_rand_alpha's form, "_" and 31 random characters. A copy that
	// streams the data ignores it and draws a random tag, as copy_obj_data
	// does.
	Tag string
}

//counterfeiter:generate . ObjectStore

// ObjectStore reads and writes objects.
type ObjectStore interface {
	// StatObject reads the head's version, xattrs and stat in one op, without
	// its data. A missing object returns a state with Exists false and no
	// error.
	StatObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey) (*ObjectState, error)
	// PrefetchObject is StatObject plus the head's first rgw_max_chunk_size
	// bytes in the same RADOS op, as RGWRados::raw_obj_stat reads them when
	// radosgw's prefetch_data asks for a first chunk. A missing object
	// returns a state with Exists false and no error.
	PrefetchObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey) (*ObjectState, error)
	// ReadObject streams rng of st to sink, in order.
	ReadObject(ctx context.Context, st *ObjectState, rng ByteRange, sink io.Writer) error
	PutObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey, body io.Reader, p PutParams) (*PutResult, error)
	// DeleteObject removes the object; a missing object is ErrNoSuchKey, which
	// the S3 op turns into 204 as radosgw does.
	DeleteObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey, p DeleteParams) error
	// CopyObject writes src's data to dstKey in dst with src's ETag.
	CopyObject(ctx context.Context, src *ObjectState, dst *BucketRecord, dstKey meta.ObjKey, p CopyParams) (*PutResult, error)
	// SetObjectAttrs sets and removes head xattrs.
	SetObjectAttrs(ctx context.Context, st *ObjectState, set map[string][]byte, rm []string) error
}
