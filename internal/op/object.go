package op

import (
	"context"
	"io"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// ObjectState is what one head read tells about an object.
type ObjectState struct {
	Bucket *BucketRecord
	// Key is the key whose head was read. A StatObject or PrefetchObject
	// that followed a versioned key's OLH sets it to the key of the version
	// it resolved, so Key.Instance is that version's id, empty for the null
	// version.
	Key    meta.ObjKey
	Exists bool
	// DeleteMarker is set on a state that does not exist because the
	// version it names is a delete marker: the current version of a key
	// named without an instance, as follow_olh sets is_dm
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9760-L9764 at v20.2.4),
	// or the version a request named.
	DeleteMarker bool
	Size         uint64
	// Mtime is the head's mtime; for a delete marker read by its version,
	// which has no head, the marker's index entry mtime.
	Mtime time.Time
	// Epoch is the version the head read returned.
	Epoch uint64
	// VersionedEpoch is set for a delete marker read by its version: its
	// index entry's versioned_epoch, which with Mtime tells the marker from
	// any other, every marker carrying the same index entry tag
	// (DeleteParams.Expect).
	VersionedEpoch uint64
	Attrs          map[string][]byte
	// Manifest is nil for an object held entirely in its head.
	Manifest    *meta.Manifest
	Compression *meta.CompressionInfo
	// Checksum is the head's user.rgw.cksum, nil when it has none or the
	// attr is not one record that decodes (cksum.DecodeAttr).
	Checksum *cksum.Cksum
	ETag     string
	// WriteTag is user.rgw.idtag.
	WriteTag     string
	StorageClass string
	ContentType  string
	// Head is the first bytes of the head object, rgw_max_chunk_size or the
	// whole head when shorter, filled by PrefetchObject; nil after StatObject.
	Head []byte
	// Version is the head's cls_version, read in the same op as the stat, as
	// RGWObjVersionTracker::prepare_op_for_read composes cls_version_read
	// into every raw_obj_stat
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L158-L167
	// and
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8843-L8845 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L166-L175
	// and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9787-L9789 at v20.2.4);
	// zero for an object that carries none.
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
	// Tag is the write tag: radosgw uses the request id
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4288 and
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L377 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4501 and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L407 at v20.2.4),
	// so user.rgw.idtag and user.rgw.tail_tag hold it NUL-terminated and the
	// index entry holds it bare. Empty selects append_rand_alpha's form, "_"
	// and 31 random characters.
	Tag string
	// ContentMD5 is the decoded Content-MD5 header, 16 bytes, nil when the
	// request carried none; a body whose MD5 differs is ErrBadDigest before
	// the head is written (RGWPutObj::execute,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4483-L4486 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4715-L4718 at v20.2.4).
	ContentMD5 []byte
	// Checksum, when set, is computed over the body and stored as the head's
	// user.rgw.cksum. A body whose checksum is not the value Expected
	// supplies is ErrBadDigest, checked after ContentMD5 and before anything
	// is written (RGWPutObj::execute,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4757-L4790 at v20.2.4).
	// A type that is not cksum.Type.Writable is ErrInvalidArgument before
	// anything is written, so no store records a type a v20.2.4 radosgw
	// cannot read.
	Checksum *ChecksumRequest
}

// PutResult describes the object a write produced.
type PutResult struct {
	ETag    string
	Size    uint64
	Mtime   time.Time
	Epoch   uint64
	Version string
	// Checksum is the checksum PutObject computed or Complete combined, as
	// the head's user.rgw.cksum stores it, and for a retried completion the
	// one the first stored; nil when the write computed none.
	Checksum *cksum.Cksum
}

// DeleteParams shapes a delete.
type DeleteParams struct {
	// IfMatch is If-Match: "*" or an ETag, unquoted first; empty means
	// unset, and a header or ETag element present with an empty value
	// arrives as EmptyCondition (storeCondition). An unversioned delete and
	// a delete marker's condition pass an object whose ETag the value begins
	// with, so EmptyCondition matches no ETag there (check_preconditions,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7287-L7306 at v20.2.4).
	// The delete of a named version compares its entry's ETag whole, reading
	// EmptyCondition as the empty string, which an entry without an ETag, a
	// delete marker's, matches
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5913-L5920 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6525-L6532 at v20.2.4).
	IfMatch string
	// Mtime is the delete time; zero means now.
	Mtime time.Time
	// UnmodifiedSince is x-amz-delete-if-unmodified-since: the object must not
	// have changed after it, compared in whole seconds; zero means unset
	// (Delete::delete_obj,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5860-L5875 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6609-L6624 at v20.2.4).
	UnmodifiedSince time.Time
	// IfMatchSize is x-amz-if-match-size: the object's size must equal it;
	// nil means unset (check_preconditions,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7264-L7269 at v20.2.4).
	IfMatchSize *uint64
	// IfMatchLastModified is x-amz-if-match-last-modified-time: the object's
	// mtime must equal it, compared in whole seconds; zero means unset
	// (check_preconditions,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7271-L7284 at v20.2.4).
	IfMatchLastModified time.Time
	// AssertEpoch, when not zero, is the RADOS version the head must still be
	// at, ObjectState.Epoch as a read returned it: the unversioned removal
	// asserts it on its write op, librados's assert_version, ahead of the
	// conditions, so a write to the head since that read, an attr write
	// included, fails the removal with ErrConcurrentModification and leaves
	// the object and its index entry. A versioned delete answers
	// ErrNotImplemented for it.
	AssertEpoch uint64
	// MarkerOwner and MarkerOwnerDisplayName are params.obj_owner, the owner
	// a delete marker's index entry names: the requester for an S3 delete,
	// the entry's owner for lifecycle
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5871-L5874 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6483-L6486 at v20.2.4).
	MarkerOwner            meta.Owner
	MarkerOwnerDisplayName string
	// SkipOLHUpdate is skip_olh_obj_update: a versioned delete leaves the
	// replay of the key's pending log to the key's next update, as
	// DeleteObjects does for all but the last delete of a key.
	SkipOLHUpdate bool
	// Expect, when not nil, is the version a versioned delete's removal must
	// still find, as its caller read it. A version with a head must still
	// carry the user.rgw.idtag Expect.Attrs holds, absent read as empty. A
	// delete marker has no head, and every marker's index entry carries the
	// same tag, so Expect must be a marker's state with the entry's
	// VersionedEpoch and Mtime, as StatObject's state of a marker read by its
	// version carries them; a marker's removal removes no data, and the null
	// marker's is refused by a second delete or a null write landing at any
	// time before its unlink. It guards the
	// removal of a named version, checked before its unlink, and, on a bucket
	// whose versioning is suspended, of the null version a delete without a
	// version replaces with the null delete marker, whose head is the key's
	// OLH head. A delete without a version on an enabled bucket removes
	// nothing, and its delete marker links only over Expect as the version
	// current, by its instance and user.rgw.idtag, held to the link as its
	// conditions are. A mismatch is ErrConcurrentModification,
	// with nothing written when it is found before the unlink or the marker's
	// link; one found by the removal after them lists the version again,
	// unless another writer removed it or a newer null write owns the null
	// version. A named version goes current again at a fresh epoch when no
	// listed version is newer, in rounds that wait out other writers' pending
	// modifications; otherwise it goes non-current at its own epoch, behind
	// the newer version listed, below every version of a key whose OLH was
	// made again since, or, when every round lost its link to another writer,
	// ahead of the current version, which then lists second. The delete runs
	// its own replay whatever SkipOLHUpdate says. The unversioned delete and
	// memstore answer ErrNotImplemented for it.
	Expect *ObjectState
}

// DeleteObjectResult is Delete::delete_obj's result: the version a versioned
// delete removed or the delete marker it wrote, "null" for the null version,
// and whether that version is a delete marker. An unversioned delete reports
// neither.
type DeleteObjectResult struct {
	VersionID    string
	DeleteMarker bool
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
	// Tag is the write tag: radosgw passes the request id
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5672 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6246 at v20.2.4),
	// under which a copy that shares the source's tails takes their
	// references and writes its head. Empty selects append_rand_alpha's form,
	// "_" and 31 random characters. A copy that streams the data ignores it
	// and draws a random tag, as copy_obj_data does.
	Tag string
}

//counterfeiter:generate . ObjectStore

// ObjectStore reads and writes objects.
type ObjectStore interface {
	// StatObject reads the head's version, xattrs and stat in one op, without
	// its data. A key named without an instance whose head is a versioned
	// key's OLH is followed to its current version, whose head's state it
	// returns. A missing object returns a state with Exists false and no
	// error.
	StatObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey) (*ObjectState, error)
	// PrefetchObject is StatObject plus the head's first rgw_max_chunk_size
	// bytes in the same RADOS op, as RGWRados::raw_obj_stat reads them when
	// radosgw's prefetch_data asks for a first chunk; after a follow, the
	// bytes of the version's head. A missing object returns a state with
	// Exists false and no error.
	PrefetchObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey) (*ObjectState, error)
	// ReadObject streams rng of st to sink, in order.
	ReadObject(ctx context.Context, st *ObjectState, rng ByteRange, sink io.Writer) error
	PutObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey, body io.Reader, p PutParams) (*PutResult, error)
	// DeleteObject removes the object; a missing object is ErrNoSuchKey, which
	// the S3 op turns into 204 as radosgw does. On a bucket whose versioning
	// is enabled or suspended, a key without an instance gets a delete marker,
	// and one with an instance, "null" included, loses that version and never
	// gets a marker
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5833-L5968 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6445-L6582 at v20.2.4).
	// A store that keeps no versions answers such a bucket ErrNotImplemented.
	// The result is nil with an error.
	DeleteObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey, p DeleteParams) (*DeleteObjectResult, error)
	// CopyObject writes src's data to dstKey in dst with src's ETag.
	CopyObject(ctx context.Context, src *ObjectState, dst *BucketRecord, dstKey meta.ObjKey, p CopyParams) (*PutResult, error)
	// SetObjectAttrs sets and removes head xattrs.
	SetObjectAttrs(ctx context.Context, st *ObjectState, set map[string][]byte, rm []string) error
}
