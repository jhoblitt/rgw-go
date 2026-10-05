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

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The head write is RGWRados::Object::Write::write_meta over _do_write_meta
// (driver/rados/rgw_rados.cc:3124-3440 at v19.2.6, :3234-3594 at v20.2.4; a
// bare line number below is v19.2.6's).

// errRetryGuarded is _do_write_meta's -EEXIST on the assume-no-entry pass
// (:3305-3308): the caller reads the head and runs the guarded pass.
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
	// never set atomic (rgw_rados.cc:6497-6505; v20.2.4 :7334-7343): no
	// guard and no idtag or tail_tag, and a reset is create(false) and
	// obj_remove whether the object exists or not. radosgw writes the
	// multipart meta object and the part heads so, without conditions,
	// and writeMeta refuses a non-atomic write that carries any.
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
	// already added (rgw_rados.cc:3359-3366; v20.2.4 :3512-3519).
	completeMultipart bool
}

// headResult is what write_meta leaves behind.
type headResult struct {
	epoch    uint64
	poolID   int64
	mtime    time.Time
	canceled bool // the write lost a race radosgw answers as success
}

// now is the gateway's clock, which stamps a write without an mtime.
func (s *Store) now() time.Time { return s.sysobj.now() }

// entryOwner is the index entry's owner: the object's ACL owner, as set_attrs
// derives it (rgw_rados.cc:6700-6706); PUT and COPY hand the driver an ACL
// whose owner is the requester, so this equals radosgw's s->owner. A missing
// or undecodable ACL gives no owner.
func entryOwner(attrs map[string][]byte) (id, displayName string) {
	b, ok := attrs[meta.AttrACL]
	if !ok {
		return "", ""
	}
	d := denc.NewDecoder(b)
	p := acl.DecodePolicy(d)
	if d.Err() != nil {
		return "", ""
	}
	return p.Owner.ID, p.Owner.DisplayName
}

// rawTag is the stored user.rgw.idtag or tail_tag bytes, NUL included, as
// bufferlist::to_str keeps them; "" when absent.
func rawTag(st *op.ObjectState, name string) string { return string(st.Attrs[name]) }

// rgwBlStr is rgw_bl_str (rgw_common.h:2063-2072 at v19.2.6): the attr's
// bytes without their trailing NULs.
func rgwBlStr(b []byte) string { return strings.TrimRight(string(b), "\x00") }

// checkPreconditions is v20.2.4's check_preconditions for a write's If-Match
// and If-None-Match (rgw_rados.cc:7286-7328 at v20.2.4), on both releases
// (docs/exclusions.md, "Write conditions are Tentacle's on Squid too"): a
// condition is unquoted with rgw_string_unquote and matches when it begins
// with the stored ETag, compared over the ETag's length (:7299, :7321).
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

// writeMeta is RGWRados::Object::Write::write_meta (rgw_rados.cc:3419-3440;
// v20.2.4 :3572-3594): without conditions an assume-no-entry pass, which
// reads nothing and creates the head exclusively; when the head exists, one
// read of it and the guarded pass. With conditions, the read and the guarded
// pass alone. Both passes share x, which prepares once. The mtime is taken
// once, as meta.set_mtime is.
func (s *Store) writeMeta(ctx context.Context, hw *headWrite, x *indexOp) (headResult, error) {
	if hw.key.Name == "" {
		// _do_write_meta's "cannot write object with empty name", -EIO (:3153-3156).
		return headResult{}, fmt.Errorf("%w: cannot write an object with an empty name in bucket %s", op.ErrUnknown, hw.rec.Info.Bucket.Name)
	}
	mtime := hw.mtime
	if mtime.IsZero() {
		mtime = s.now()
	}
	if hw.nonAtomic {
		if hw.ifMatch != "" || hw.ifNoneMatch != "" {
			// radosgw's non-atomic writers, RadosMultipartUpload::init and
			// MultipartObjectProcessor::complete, set no conditions
			// (rgw_putobj_processor.cc:515-529 at v19.2.6), so a caller that
			// passes them asks for a write radosgw never makes.
			return headResult{}, fmt.Errorf("%w: a non-atomic head write of %s carries write conditions", op.ErrInternalError, hw.key.Name)
		}
		// Without conditions write_meta's only pass assumes no entry
		// (:3429-3436), and its create(false) cannot fail with EEXIST.
		return s.doWriteMeta(ctx, hw, x, &op.ObjectState{Bucket: hw.rec, Key: hw.key}, mtime, true)
	}
	assumeNoent := hw.ifMatch == "" && hw.ifNoneMatch == ""
	if assumeNoent {
		res, err := s.doWriteMeta(ctx, hw, x, &op.ObjectState{Bucket: hw.rec, Key: hw.key}, mtime, true)
		if !errors.Is(err, errRetryGuarded) {
			return res, err
		}
	}
	st, err := s.readHead(ctx, hw.rec, hw.key, false)
	if err != nil {
		return headResult{}, err
	}
	return s.doWriteMeta(ctx, hw, x, st, mtime, false)
}

// doWriteMeta is one pass of _do_write_meta (rgw_rados.cc:3124-3417; v20.2.4
// :3234-3570) over st, the head as read, or as missing on the
// assume-no-entry pass.
func (s *Store) doWriteMeta(ctx context.Context, hw *headWrite, x *indexOp, st *op.ObjectState, mtime time.Time, assumeNoent bool) (headResult, error) {
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
		if gerr := s.guardHead(w, hw, st); gerr != nil {
			return headResult{}, gerr
		}
		if hw.create {
			if st.Exists {
				// remove_rgw_head_obj keeps the olh attrs (rgw_rados.cc:5722-5727).
				w.Create(false)
				rgw.ObjRemove(w, []string{meta.AttrOLHPrefix}, s.release)
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
	w.SetMtime(mtime)
	if hw.data != nil {
		w.WriteFull(hw.data)
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
		// A multipart part's head is immutable (:3272-3278).
		origExists, origSize = false, 0
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
		return s.cancelWrite(hw, x, err)
	}
	if st.Manifest != nil && !hw.keepTail {
		s.completeAtomicModification(ctx, hw.rec, hw.key, st)
	}
	owner, display := entryOwner(attrs)
	x.complete(rgw.EntryVer{Pool: ref.pool.ID(), Epoch: epoch}, rgw.DirEntryMeta{
		Category: cmp.Or(hw.category, rgw.CategoryMain), Size: hw.size, AccountedSize: hw.accountedSize, Mtime: mtime,
		ETag: etag, Owner: owner, OwnerDisplayName: display, ContentType: contentType, StorageClass: storageClass,
	})
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
	return headResult{epoch: epoch, poolID: ref.pool.ID(), mtime: mtime}, nil
}

// headWriteRef is where hw writes: the object its pool override names, or
// the key's head in the bucket's data pool.
func (s *Store) headWriteRef(ctx context.Context, hw *headWrite) (objRef, error) {
	if hw.pool == nil {
		return s.headRef(ctx, hw.rec, hw.key)
	}
	return objRef{pool: hw.pool, oid: hw.oid, loc: hw.loc}, nil
}

// guardHead checks the write's conditions, then adds
// prepare_atomic_modification's guard (rgw_rados.cc:6493-6513 at v19.2.6;
// :3298-3300 and :7345-7349 at v20.2.4): a cmpxattr on the head's write tag
// when the head has a manifest or a tag radosgw did not fake. Neither release
// compares the tag for If-None-Match: *, which the exclusive create guards,
// and Tentacle guards no versioned instance. Squid's guard also covers any
// write with conditions, which only its own condition checks rely on, and
// those are v20.2.4's here.
func (s *Store) guardHead(w *radosclient.WriteOp, hw *headWrite, st *op.ObjectState) error {
	if err := s.checkPreconditions(st, hw.ifMatch, hw.ifNoneMatch); err != nil {
		return err
	}
	// get_obj_state_impl fakes a tag for a head with a manifest and none
	// (:6238-6245); a fake tag is never compared.
	fake := st.Manifest != nil && st.WriteTag == ""
	guard := (st.Manifest != nil || st.WriteTag != "") && !fake
	if s.release >= denc.Tentacle && hw.key.Instance != "" {
		guard = false
	}
	if guard && hw.ifNoneMatch != "*" {
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, st.Attrs[meta.AttrIDTag])
	}
	return nil
}

// completeAtomicModification is complete_atomic_modification
// (rgw_rados.cc:5382-5407): the overwritten head's tails go to the GC under
// its tail_tag, or, when it has none, its object tag: its idtag, or the tag
// radosgw fakes for a head with a manifest and no idtag. A chain that cannot
// be built is logged and left, as radosgw logs complete_atomic_modification's
// failure without failing the write (:3314-3317). st is key's head in rec's
// bucket as the write or the delete read it.
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

// fakeTag is generate_fake_tag (rgw_rados.cc:6052-6083 at v19.2.6,
// :6806-6837 at v20.2.4), the object tag get_obj_state_impl gives a head with
// a manifest and no idtag (:6238-6245): the oid of the manifest's first
// stripe, or of its second when it has a tail, and "_", then the hex MD5 of
// the stored manifest attr and the etag attr, NUL-terminated. st.Manifest is
// the manifest after set_head, as radosgw walks it.
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

// cancelWrite is done_cancel (rgw_rados.cc:3369-3416; v20.2.4 :3522-3569):
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
