package driver

import (
	"cmp"
	"context"
	"crypto/md5" //nolint:gosec // generate_fake_tag's digest, not a security boundary
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The head write is RGWRados::Object::Write::write_meta over _do_write_meta
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3124-L3440 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3234-L3594 at v20.2.4).

// errRetryGuarded is _do_write_meta's -EEXIST on the assume-no-entry pass
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3305-L3308):
// the caller reads the head and runs the guarded pass.
var errRetryGuarded = errors.New("driver: object exists; retry guarded")

// headWrite is RGWRados::Object::Write::meta for one write_meta call.
type headWrite struct {
	rec                  *op.BucketRecord
	key                  meta.ObjKey
	tag                  string
	data                 []byte            // the head's bytes; written with write_full even when empty
	manifest             *meta.Manifest    // nil for set_attrs-style writes
	attrs                map[string][]byte // set in byte order of the names; empty values skipped
	rmAttrs              []string
	mtime                time.Time // zero means now
	ifMatch, ifNoneMatch string
	create               bool // PUT_OBJ_CREATE: reset the object
	modifyTail, keepTail bool
	size, accountedSize  uint64
	// nonAtomic takes prepare_atomic_modification's branch for an object
	// never set atomic
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6497-L6505;
	// v20.2.4
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7334-L7343):
	// no guard and no idtag or tail_tag, and a reset is create(false)
	// and obj_remove whether the object exists or not. radosgw writes
	// the multipart meta object and the part heads so, without
	// conditions, and writeMeta refuses a non-atomic write that carries
	// any.
	nonAtomic bool
	// pool, oid and loc place the head where the key's oid in the bucket's
	// data pool would not: the meta object in the data-extra pool, a part
	// head in its upload's tail pool. As in an objRef, the pool handle
	// already carries loc when there is one. A nil pool keeps headRef's
	// placement.
	pool     radosclient.Pool
	oid, loc string
	// category is the index entry's; 0 is Main.
	category uint8
	// completeMultipart adds no bytes to the quota cache, which the parts
	// already added
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3359-L3366;
	// v20.2.4
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3512-L3519).
	completeMultipart bool
	// expect is the head as the caller read it, for a write that is only
	// safe on that head: there is no assume-no-entry pass and no read of
	// its own, the guard compares against expect, and a head replaced or
	// removed since fails the write with the guard's error instead of
	// counting as success. radosgw has no such write; a copy onto itself
	// needs it (docs/exclusions.md, "Object write differences").
	expect *op.ObjectState
	// incompressible marks data radosgw's compressor made: its write_full is
	// followed by the incompressible hint, as _do_write_meta follows it for
	// an object set_compressed marked
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3230-L3238 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3327-L3335 at v20.2.4).
	incompressible bool
	// olhEpoch is meta.olh_epoch. Set, a write that is a versioned op links
	// its version under the key's OLH at that epoch, 0 letting the class
	// take the OLH entry's next (put_versioned.go). Nil links nothing and
	// keeps the unversioned head write: radosgw gives no epoch to the
	// multipart meta object or a part head.
	olhEpoch *uint64
}

// headResult is what write_meta leaves behind.
type headResult struct {
	epoch    uint64
	poolID   int64
	mtime    time.Time
	canceled bool // the write lost a race radosgw answers as success
	// written reports a head write that landed: the head names the write's
	// tails whatever its completion or its link then answered.
	written bool
}

// now is the gateway's clock, which stamps a write without an mtime.
func (s *Store) now() time.Time { return s.sysobj.now() }

// entryOwner is the owner of the ACL in attrs, decoded as set_attrs decodes
// it for the index entry, through decode_policy, which reads the owner alone
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6694-L6700 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7494-L7500 at v20.2.4):
// a policy whose grants do not decode still names its owner, and a missing
// ACL or one whose owner does not decode names none. A PUT or COPY's index
// entry takes radosgw's request owner instead (obj_op.meta.owner,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L382,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L522
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L743 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L412,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L558
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L787 at v20.2.4),
// the owner the ACL the ops hand the driver is built with, so both read
// alike.
func entryOwner(attrs map[string][]byte) (id, displayName string) {
	o, err := policyOwner(attrs[meta.AttrACL])
	if err != nil {
		return "", ""
	}
	return o.ID, o.DisplayName
}

// rawTag is the stored user.rgw.idtag or tail_tag bytes, NUL included, as
// bufferlist::to_str keeps them; "" when absent.
func rawTag(st *op.ObjectState, name string) string { return string(st.Attrs[name]) }

// rgwBlStr is rgw_bl_str
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L2063-L2072 at v19.2.6):
// the attr's bytes without their trailing NULs.
func rgwBlStr(b []byte) string { return strings.TrimRight(string(b), "\x00") }

// checkPreconditions is check_preconditions for a write's If-Match and
// If-None-Match
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7286-L7328 at v20.2.4,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6672-L6714 at v19.2.7),
// which both releases apply to a PutObject alike. A
// CompleteMultipartUpload's conditions reach it on Squid too, where v19.2.7
// reads none (docs/exclusions.md, "Write conditions are Tentacle's on Squid
// too"). A condition is unquoted with rgw_string_unquote and matches when it
// begins with the stored ETag, compared over the ETag's length
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7299,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7321 at v20.2.4).
// If-Match on a missing object is NoSuchKey; If-None-Match with an ETag
// passes an object that has none.
func (s *Store) checkPreconditions(st *op.ObjectState, ifMatch, ifNoneMatch string) error {
	etag, hasETag := st.Attrs[meta.AttrETag]
	if ifMatch != "" {
		switch {
		case ifMatch == "*":
			if !st.Exists {
				return op.ErrNoSuchKey
			}
		case !hasETag:
			if !st.Exists {
				return op.ErrNoSuchKey
			}
			return op.ErrPreconditionFailed
		case !strings.HasPrefix(op.Unquote(ifMatch), string(etag)):
			return op.ErrPreconditionFailed
		}
	}
	if ifNoneMatch != "" {
		switch {
		case ifNoneMatch == "*":
			if st.Exists {
				return op.ErrPreconditionFailed
			}
		case hasETag && strings.HasPrefix(op.Unquote(ifNoneMatch), string(etag)):
			return op.ErrPreconditionFailed
		}
	}
	return nil
}

// writeMeta is RGWRados::Object::Write::write_meta
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3419-L3440;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3572-L3594):
// without conditions an assume-no-entry pass, which reads nothing and
// creates the head exclusively; when the head exists, one read of it and the
// guarded pass. With conditions, the read and the guarded pass alone, its
// conditions checked on condState's state. Both passes share x, which
// prepares once. The mtime is taken once, as meta.set_mtime is, and the
// attrs are finalized once with it (finalizeHeadAttrs).
func (s *Store) writeMeta(ctx context.Context, hw *headWrite, x *indexOp) (headResult, error) {
	if hw.key.Name == "" {
		// _do_write_meta's "cannot write object with empty name", -EIO
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3153-L3156).
		return headResult{}, fmt.Errorf("%w: cannot write an object with an empty name in bucket %s", op.ErrUnknown, hw.rec.Info.Bucket.Name)
	}
	mtime := hw.mtime
	if mtime.IsZero() {
		mtime = s.now()
	}
	if hw.nonAtomic && (hw.ifMatch != "" || hw.ifNoneMatch != "") {
		// radosgw's non-atomic writers, RadosMultipartUpload::init and
		// MultipartObjectProcessor::complete, set no conditions on Squid
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L515-L529 at v19.2.6).
		// Tentacle's complete passes UploadPart's
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L563-L564 at v20.2.4),
		// which rgw-go ignores (docs/exclusions.md, "UploadPart ignores
		// If-Match and If-None-Match"), so a caller that passes them asks
		// for a write rgw-go never makes.
		return headResult{}, fmt.Errorf("%w: a non-atomic head write of %s carries write conditions", op.ErrInternalError, hw.key.Name)
	}
	// PUT_OBJ_CREATE alone: the multipart meta object's create is
	// PUT_OBJ_CREATE_EXCL
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3308 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4148 at v20.2.4).
	hw.attrs = s.finalizeHeadAttrs(hw.rec, mtime, hw.create && !hw.nonAtomic, hw.attrs)
	missing := &op.ObjectState{Bucket: hw.rec, Key: hw.key}
	if hw.nonAtomic {
		// Without conditions write_meta's only pass assumes no entry
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3429-L3436),
		// and its create(false) cannot fail with EEXIST.
		return s.doWriteMeta(ctx, hw, x, missing, missing, mtime, true, nil)
	}
	if hw.expect != nil {
		return s.doWriteMeta(ctx, hw, x, hw.expect, hw.expect, mtime, false, nil)
	}
	assumeNoent := hw.ifMatch == "" && hw.ifNoneMatch == ""
	if assumeNoent {
		res, err := s.doWriteMeta(ctx, hw, x, missing, missing, mtime, true, nil)
		if !errors.Is(err, errRetryGuarded) {
			return res, err
		}
	}
	st, err := s.readWriteHead(ctx, hw)
	if err != nil {
		return headResult{}, err
	}
	if nullCondWrite(hw, st) {
		return s.writeNullVersion(ctx, hw, x, st, mtime)
	}
	cur, err := s.condState(ctx, hw, st)
	if err != nil {
		return headResult{}, err
	}
	return s.doWriteMeta(ctx, hw, x, st, cur, mtime, false, nil)
}

// doWriteMeta is one pass of _do_write_meta
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3124-L3417;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3234-L3570)
// over st, the head as read, or as missing on the assume-no-entry pass, the
// conditions checked on cur. A versioned op (versionedOp) carries
// VERSIONED_OP on its index op, sets the OLH tag of an OLH head it writes
// over again, and once its head write lands adds to the quota cache and
// links its version (linkVersion); any other write completes its index entry
// unawaited. in, when not nil, is the init of a conditional null write's
// modification on st, which the head write's op carries (nullInit): its
// guards come first, its sets after the reset, a refusal of its guards
// answers errOLHGuardRefused with nothing written, and the link is made under
// its op tag.
func (s *Store) doWriteMeta(ctx context.Context, hw *headWrite, x *indexOp, st, cur *op.ObjectState, mtime time.Time, assumeNoent bool, in *nullInit) (headResult, error) {
	ref, err := s.headWriteRef(ctx, hw)
	if err != nil {
		return headResult{}, err
	}
	w := radosclient.NewWriteOp()
	if hw.nonAtomic {
		if hw.create {
			w.Create(false)
			rgw.ObjRemove(w, []string{meta.AttrOLHPrefix}, s.release)
		}
	} else {
		if gerr := s.guardHead(w, hw, st, cur, in); gerr != nil {
			return headResult{}, gerr
		}
		if hw.create {
			if st.Exists {
				w.Create(false)
				rgw.ObjRemove(w, headKeptAttrs(hw.key), s.release)
			} else {
				w.Create(true)
			}
		}
		tag := []byte(hw.tag + "\x00")
		w.SetXattr(meta.AttrIDTag, tag)
		if hw.modifyTail {
			w.SetXattr(meta.AttrTailTag, tag)
		}
	}
	if isOLHHead(st) {
		// _do_write_meta sets the OLH tag it read again, which the reset
		// keeps anyway
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3223-L3225 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3320-L3322 at v20.2.4).
		w.SetXattr(meta.AttrOLHIDTag, in.olhIDTag(st))
	}
	if in != nil {
		in.addSets(w, st, s.release)
	}
	w.SetMtime(mtime)
	if hw.data != nil {
		w.WriteFull(hw.data)
		if hw.incompressible {
			w.SetAllocHint(0, 0, allocHintIncompressible)
		}
	}
	for _, name := range hw.rmAttrs {
		w.RmXattr(name)
	}
	attrs := maps.Clone(hw.attrs)
	var storageClass string
	if hw.manifest != nil {
		storageClass = hw.manifest.TailPlacement.PlacementRule.StorageClass
		delete(attrs, meta.AttrManifest)
		w.SetXattr(meta.AttrManifest, encodeAt(hw.manifest, s.release))
	}
	var etag, contentType string
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		v := attrs[name]
		if len(v) == 0 {
			continue
		}
		w.SetXattr(name, v)
		switch name {
		case meta.AttrETag:
			etag = rgwBlStr(v)
		case meta.AttrContentType:
			contentType = rgwBlStr(v)
		}
	}
	if _, ok := attrs[meta.AttrPGVer]; !ok {
		rgw.ObjStorePGVer(w, meta.AttrPGVer, s.release)
	}
	if _, ok := attrs[meta.AttrSourceZone]; !ok {
		w.SetXattr(meta.AttrSourceZone, binary.LittleEndian.AppendUint32(nil, s.w.shortZoneID))
	}
	if storageClass != "" {
		w.SetXattr(meta.AttrStorageClass, []byte(storageClass))
	}
	origExists, origSize := st.Exists, st.Size
	if st.Compression != nil {
		origSize = st.Compression.OrigSize
	}
	if !hw.create {
		// A multipart part's head is immutable
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3272-L3278).
		origExists, origSize = false, 0
	}
	versioned := versionedOp(hw, st)
	if versioned {
		x.bilogFlags |= rgw.BILogFlagVersionedOp
	}
	if !x.prepared {
		if err = x.prepare(ctx, rgw.OpAdd); err != nil {
			return headResult{}, op.FromRADOS(err, op.ScopeObject)
		}
	}
	epoch, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagNone)
	if err != nil {
		if assumeNoent && !hw.nonAtomic && errors.Is(err, radosclient.ErrExists) {
			return headResult{}, errRetryGuarded
		}
		if in != nil && s.olhGuardRefused(ctx, hw, st, err) {
			return headResult{}, errOLHGuardRefused
		}
		return s.cancelWrite(hw, x, err)
	}
	if st.Manifest != nil && !hw.keepTail {
		s.completeAtomicModification(ctx, hw.rec, hw.key, st)
	}
	owner, display := entryOwner(attrs)
	ver := rgw.EntryVer{Pool: ref.pool.ID(), Epoch: epoch}
	m := rgw.DirEntryMeta{
		Category: cmp.Or(hw.category, rgw.CategoryMain), Size: hw.size, AccountedSize: hw.accountedSize, Mtime: mtime,
		ETag: etag, Owner: owner, OwnerDisplayName: display, ContentType: contentType, StorageClass: storageClass,
	}
	if !versioned {
		s.completePlain(ctx, hw.rec, hw.key, x, ver, m)
	}
	added := int64(1)
	if origExists {
		added = 0
	}
	addBytes := int64(hw.accountedSize) //nolint:gosec // object sizes are far below 2^63
	if hw.completeMultipart {
		addBytes = 0
	}
	if err := s.AdjustStats(ctx, hw.rec, hw.rec.Info.Owner, added, addBytes, int64(origSize)); err != nil { //nolint:gosec // object sizes are far below 2^63
		slog.WarnContext(ctx, "quota cache adjustment failed", slog.String("bucket", hw.rec.Info.Bucket.Name), slog.Any("error", err))
	}
	res := headResult{epoch: epoch, poolID: ref.pool.ID(), mtime: mtime, written: true}
	if versioned {
		// The cache is adjusted before the link, where radosgw adjusts it
		// only once set_olh has succeeded
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3369-L3398 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3490-L3519 at v20.2.4),
		// so that the removal of a version the link could not make, or
		// voided, takes back what the write added.
		return res, s.linkVersion(ctx, hw, x, ver, m, in)
	}
	return res, nil
}

// headWriteRef is where hw writes: the object its pool override names, or
// the key's head in the bucket's data pool.
func (s *Store) headWriteRef(ctx context.Context, hw *headWrite) (objRef, error) {
	if hw.pool == nil {
		return s.headRef(ctx, hw.rec, hw.key)
	}
	return objRef{pool: hw.pool, oid: hw.oid, loc: hw.loc}, nil
}

// guardHead checks the write's conditions on cur, condState's state, then
// adds to w prepare_atomic_modification's guard on st, the head as read
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3201-L3202
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6731-L6735 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3298-L3300
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7345-L7349 at v20.2.4):
// a cmpxattr on the head's write tag when the head has a manifest or a tag
// radosgw did not fake (tagGuarded). Both releases guard alike: neither
// compares the tag for If-None-Match: *, which the exclusive create guards,
// or for a versioned instance. in, when not nil, puts the guards of the
// init the head write carries on the OLH head st is ahead of that guard
// (nullInit).
func (s *Store) guardHead(w *radosclient.WriteOp, hw *headWrite, st, cur *op.ObjectState, in *nullInit) error {
	if err := s.checkPreconditions(cur, hw.ifMatch, hw.ifNoneMatch); err != nil {
		return err
	}
	if hw.expect != nil {
		// The expected head is always compared: on its write tag, or, for a
		// head whose tag radosgw would fake, on its stored manifest, which
		// names the tails the write keeps.
		if tag := st.Attrs[meta.AttrIDTag]; len(tag) > 0 {
			w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, tag)
		} else {
			w.CmpXattr(meta.AttrManifest, radosclient.CmpEQ, st.Attrs[meta.AttrManifest])
		}
		return nil
	}
	if in != nil {
		in.addGuards(w, st, s.release)
	}
	if tagGuarded(hw, st) {
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, st.Attrs[meta.AttrIDTag])
	}
	return nil
}

// tagGuarded reports whether hw's head write over st compares the head's
// write tag. get_obj_state_impl fakes a tag for a head with a manifest and
// none
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6238-L6245);
// a fake tag is never compared.
func tagGuarded(hw *headWrite, st *op.ObjectState) bool {
	fake := st.Manifest != nil && st.WriteTag == ""
	guard := (st.Manifest != nil || st.WriteTag != "") && !fake
	return guard && hw.key.Instance == "" && hw.ifNoneMatch != "*"
}

// completeAtomicModification is complete_atomic_modification
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5382-L5407):
// the overwritten head's tails go to the GC under its tail_tag, or, when it
// has none, its object tag: its idtag, or the tag radosgw fakes for a head
// with a manifest and no idtag. A chain that cannot be built is logged and
// left, as radosgw logs complete_atomic_modification's failure without
// failing the write
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3314-L3317).
// st is key's head in rec's bucket as the write or the delete read it.
func (s *Store) completeAtomicModification(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, st *op.ObjectState) {
	chain, err := s.gcChain(st.Manifest, rec.Info.PlacementRule)
	if err != nil {
		slog.ErrorContext(ctx, "the overwritten object's tails cannot be listed; they are leaked",
			slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name), slog.Any("error", err))
		return
	}
	if len(chain) == 0 {
		return
	}
	tag := rawTag(st, meta.AttrTailTag)
	if tag == "" {
		tag = rawTag(st, meta.AttrIDTag)
	}
	if tag == "" {
		tag = fakeTag(st)
	}
	// The head is already replaced or removed: its old tails are queued even
	// when the request's client has gone.
	s.enqueueGC(context.WithoutCancel(ctx), chain, tag)
}

// fakeTag is generate_fake_tag
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6052-L6083 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6806-L6837 at v20.2.4),
// the object tag get_obj_state_impl gives a head with a manifest and no idtag
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6238-L6245):
// the oid of the manifest's first stripe, or of its second when it has a
// tail, and "_", then the hex MD5 of the stored manifest attr and the etag
// attr, NUL-terminated. st.Manifest is the manifest after set_head, as
// radosgw walks it.
func fakeTag(st *op.ObjectState) string {
	var tag string
	if it, err := st.Manifest.Seek(0); err == nil && !it.Done() {
		// "first object usually points at the head, let's skip to a more
		// unique part"
		if !st.Manifest.HasTail() || it.Next() == nil {
			obj, _, _ := it.Location()
			tag = meta.Stripe{Obj: obj}.OID() + "_"
		}
	}
	h := md5.New() //nolint:gosec // generate_fake_tag's digest, not a security boundary
	h.Write(st.Attrs[meta.AttrManifest])
	h.Write(st.Attrs[meta.AttrETag])
	return tag + hex.EncodeToString(h.Sum(nil)) + "\x00"
}

// completePlain sends the ADD completion of a write to key of rec that links
// no version: a head write's, or an attr write's (SetObjectAttrs). The write
// of an object's plain or null entry, its key in no namespace and its
// instance empty or null, on a bucket whose flags carry BUCKET_VERSIONED,
// awaits it (indexOp.completeSync), under a context its caller's end does not
// cancel, as the completion manager's is not: the key's next delete there
// links the null delete marker, which takes the pending op of the null entry
// it replaces, and a completion landing after that link clears the op from
// the null data key alone, leaving the marker pending for good
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1349-L1357 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1476-L1484 at v20.2.4;
// docs/cgo-limitations.md, "Bucket index completions to one shard can land
// out of order"). A failure is logged and left for listing to reconcile, as
// the unawaited completion leaves it. Any other write sends it without
// waiting, as radosgw does.
func (s *Store) completePlain(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, x *indexOp, ver rgw.EntryVer, m rgw.DirEntryMeta) {
	if !rec.Info.Versioned() || key.NS != "" || key.Instance != "" && key.Instance != meta.NullInstance {
		x.complete(ver, m)
		return
	}
	if err := x.completeSync(context.WithoutCancel(ctx), ver, m); err != nil {
		slog.WarnContext(ctx, "bucket index completion failed; listing reconciles the entry",
			slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name), slog.Any("error", err))
	}
}

// cancelWrite is done_cancel
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3369-L3416;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3522-L3569):
// the index entry is canceled unless the write timed out, which may yet
// land, then the race is judged. Without conditions a replaced, removed or
// created head is success; If-Match: * fails on a removed head and succeeds
// on a replaced one; If-None-Match: * fails on a created head and succeeds
// on a removed one. The cancel is not awaited.
func (s *Store) cancelWrite(hw *headWrite, x *indexOp, err error) (headResult, error) {
	canceled := false
	if !errors.Is(err, radosclient.ErrTimedOut) {
		x.cancel()
		canceled = true
	}
	res := headResult{canceled: canceled}
	if hw.expect != nil {
		// A replaced head is ECANCELED, ConcurrentModification, and a removed
		// one ENOENT, NoSuchKey.
		return res, op.FromRADOS(err, op.ScopeObject)
	}
	if hw.ifMatch == "" && hw.ifNoneMatch == "" {
		if errors.Is(err, radosclient.ErrCanceled) || errors.Is(err, radosclient.ErrNotFound) || errors.Is(err, radosclient.ErrExists) {
			return res, nil
		}
		return res, op.FromRADOS(err, op.ScopeObject)
	}
	// Each condition judges r as the one before it left it.
	r := err
	if hw.ifMatch == "*" {
		switch {
		case errors.Is(r, radosclient.ErrNotFound):
			r = op.ErrPreconditionFailed
		case errors.Is(r, radosclient.ErrCanceled):
			r = nil
		}
	}
	if hw.ifNoneMatch == "*" {
		switch {
		case errors.Is(r, radosclient.ErrExists):
			r = op.ErrPreconditionFailed
		case errors.Is(r, radosclient.ErrNotFound):
			r = nil
		}
	}
	return res, op.FromRADOS(r, op.ScopeObject)
}
