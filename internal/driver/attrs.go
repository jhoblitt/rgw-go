package driver

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// restoreTemporary is rgw::sal::RGWRestoreType::Temporary (rgw_sal.h:178-182
// at v20.2.4), which user.rgw.restore-type holds as its single byte.
const restoreTemporary = 1

// SetObjectAttrs is RGWRados::set_attrs (driver/rados/rgw_rados.cc:6593-6757
// at v19.2.6, :7393-7593 at v20.2.4; a bare line number below is v19.2.6's)
// as RadosObject::set_obj_attrs calls it with FLAG_LOG_OP, which
// modify_obj_attrs and delete_obj_attrs always pass
// (driver/rados/rgw_sal_rados.cc:2378-2429 at v19.2.6, :2971-3022 at
// v20.2.4). st is the state the op's Init read, as radosgw's object context
// holds it; a lost race is ErrConcurrentModification.
func (s *Store) SetObjectAttrs(ctx context.Context, st *op.ObjectState, set map[string][]byte, rm []string) error {
	rec := st.Bucket
	key := st.Key
	if key.Instance == "null" {
		key.Instance = ""
	}
	ref, err := s.headRef(ctx, rec, key)
	if err != nil {
		return err
	}
	w := radosclient.NewWriteOp()
	// append_atomic_test (:6457-6472): the tag guards the write unless the
	// head has none, or has the one get_obj_state_impl fakes for a head with
	// a manifest, which st.WriteTag leaves empty.
	if st.WriteTag != "" {
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, st.Attrs[meta.AttrIDTag])
	}
	// "ensure null version object exist" (:6619-6622).
	if st.Key.Instance == "null" && st.Manifest == nil {
		return op.ErrNoSuchKey
	}
	// rmattrs and attrs are std::maps: each name once, in byte order.
	for _, name := range slices.Compact(slices.Sorted(slices.Values(rm))) {
		w.RmXattr(name)
	}
	for _, name := range slices.Sorted(maps.Keys(set)) {
		if v := set[name]; len(v) > 0 {
			w.SetXattr(name, v)
		}
	}
	// A guard alone is enough to send the op (:6658-6659).
	if len(w.Steps()) == 0 {
		return nil
	}
	x := s.newIndexOp(rec, key, "")
	if err = x.prepare(ctx, rgw.OpAdd); err != nil {
		return op.FromRADOS(err, op.ScopeObject)
	}
	w.SetXattr(meta.AttrIDTag, []byte(x.tag+"\x00"))
	// "make a tiny adjustment to the existing mtime so that
	// fetch_remote_obj() won't return ERR_NOT_MODIFIED when syncing the
	// modified object" (rgw_sal_rados.cc:2382-2384).
	mtime := st.Mtime.Add(time.Nanosecond)
	w.SetMtime(mtime)
	epoch, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagNone)
	if err != nil {
		// Unlike a PUT's head write, set_attrs cancels whatever the failure,
		// a timeout included (:6726-6731).
		x.cancel()
		return op.FromRADOS(err, op.ScopeObject)
	}
	x.complete(rgw.EntryVer{Pool: ref.pool.ID(), Epoch: epoch}, s.attrsEntry(st, set, mtime))
	return nil
}

// attrsEntry is the index entry set_attrs completes with (:6693-6725;
// v20.2.4 :7494-7561): the owner, ETag, content type and storage class from
// set when it names them, even empty, and from st's attrs otherwise; st's
// size and accounted size; the nudged mtime.
func (s *Store) attrsEntry(st *op.ObjectState, set map[string][]byte, mtime time.Time) rgw.DirEntryMeta {
	pick := func(name string) []byte {
		if v, ok := set[name]; ok {
			return v
		}
		return st.Attrs[name]
	}
	owner, display := entryOwner(map[string][]byte{meta.AttrACL: pick(meta.AttrACL)})
	accounted := st.Size
	if st.Compression != nil {
		accounted = st.Compression.OrigSize
	}
	m := rgw.DirEntryMeta{
		Category: rgw.CategoryMain, Size: st.Size, AccountedSize: accounted, Mtime: mtime,
		ETag: rgwBlStr(pick(meta.AttrETag)), ContentType: rgwBlStr(pick(meta.AttrContentType)),
		StorageClass: rgwBlStr(pick(meta.AttrStorageClass)),
		Owner:        owner, OwnerDisplayName: display,
	}
	if s.release >= denc.Tentacle {
		s.restoreCategory(&m, set)
	}
	return m
}

// restoreCategory is v20.2.4's "Retain Object category as CloudTiered while
// restore is in progress or failed or if its temporarily restored copy"
// (:7524-7557 at v20.2.4), which reads set alone, never the stored attrs. A
// value too short to decode throws, which radosgw catches with the category
// as it then stands.
func (s *Store) restoreCategory(m *rgw.DirEntryMeta, set map[string][]byte) {
	status, ok := set[meta.AttrRestoreStatus]
	if !ok || len(status) == 0 {
		return
	}
	if meta.RestoreStatus(status[0]) != meta.CloudRestored {
		m.Category = rgw.CategoryCloudTiered
		return
	}
	rt, ok := set[meta.AttrRestoreType]
	if !ok || len(rt) == 0 || rt[0] != restoreTemporary {
		return
	}
	m.Category = rgw.CategoryCloudTiered
	if sc, ok := set[meta.AttrCloudTierStorageClass]; ok {
		m.StorageClass = rgwBlStr(sc)
	}
}
