package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// sysObj names a metadata object.
type sysObj struct {
	pool meta.Pool
	oid  string
}

// sysobjReadSize is the buffer a metadata read starts with. radosgw reads a
// system object whole into a bufferlist; the seam reads into a buffer sized
// up front (docs/cgo-limitations.md, "A read needs its buffer sized up
// front"), and librados charges the objecter's byte budget the length asked
// for, so a read starts small and reads an object its stat shows larger
// again at that size.
const sysobjReadSize = 4 << 10

// sysobjReadTries bounds the reads of an object that keeps growing past the
// size the last read's stat reported.
const sysobjReadTries = 4

// sysobjs is RGWSI_SysObj_Cache over RGWSI_SysObj_Core
// (services/svc_sys_obj_cache.cc and svc_sys_obj_core.cc at v19.2.6 and
// v20.2.4): metadata objects read through the cache and written with a
// cache notify to every gateway.
type sysobjs struct {
	pools   *poolCache
	cache   *objectCache
	notify  *notifier
	release denc.Release
	now     func() time.Time
}

// openSysObj builds the metadata cache and the notifier on the zone's
// control pool and creates the control objects, as radosgw's notify service
// does at start (init_watch, services/svc_notify.cc:199-264 at v19.2.6,
// :162-224 at v20.2.4), whose failure fails radosgw's start. The cache
// stays disabled until every control watch is registered, and for good when
// rgw_cache_enabled is false, while the notifier still watches and sends
// (docs/exclusions.md).
func openSysObj(ctx context.Context, pools *poolCache, params meta.ZoneParams, o options, r denc.Release) (*sysobjs, error) {
	cache := newObjectCache(o.cacheLRUSize, o.cacheExpiry, params.DomainRoot, time.Now)
	control, err := pools.get(ctx, params.ControlPool)
	if err != nil {
		return nil, err
	}
	onEnabled := cache.setEnabled
	if !o.cacheEnabled {
		onEnabled = func(bool) {}
	}
	n := newNotifier(control, controlOIDs(o.numControlOIDs), r, o.maxNotifyRetries, cache.onNotify, onEnabled)
	if err := n.createControlObjects(ctx); err != nil {
		return nil, err
	}
	return &sysobjs{pools: pools, cache: cache, notify: n, release: r, now: time.Now}, nil
}

// readParams says what a read returns. attrs are the user.rgw. xattrs
// unless rawAttrs asks for every one, as radosgw's raw_attrs does. With objv
// the read checks the version objv read before, when it reads RADOS, and
// leaves objv holding the version read.
type readParams struct {
	data, attrs, meta bool
	rawAttrs          bool
	objv              *objv
}

// readResult is what a read returns: each part only when readParams asked
// for it, the size and mtime with meta and the version with objv.
type readResult struct {
	data    []byte
	attrs   map[string][]byte
	size    uint64
	mtime   time.Time
	version meta.ObjVersion
}

// read is RGWSI_SysObj_Cache::read over RGWSI_SysObj_Core::read
// (svc_sys_obj_cache.cc:113-240, svc_sys_obj_core.cc:129-203): a hit
// serves the entry, a cached negative is ENOENT, and a miss reads RADOS and
// caches what it read, ENOENT as a negative entry. A miss asks RADOS for the
// stat and every xattr whatever p asks for, so the entry serves a later
// read of them too.
func (s *sysobjs) read(ctx context.Context, o sysObj, p readParams) (readResult, error) {
	name := normalName(o.pool, o.oid)
	var mask uint32
	if p.data {
		mask |= meta.CacheFlagData
	}
	if p.attrs {
		mask |= meta.CacheFlagXattrs
	}
	if p.meta {
		mask |= meta.CacheFlagMeta
	}
	if p.objv != nil {
		mask |= meta.CacheFlagObjVersion
	}
	info, err := s.cache.get(name, mask)
	switch {
	case errors.Is(err, errNegativeEntry):
		return readResult{}, fmt.Errorf("reading %s/%s: %w", o.pool, o.oid, err)
	case err != nil:
		if info, err = s.readMiss(ctx, o, name, p); err != nil {
			return readResult{}, err
		}
	}
	var res readResult
	if p.data {
		res.data = info.data
	}
	switch {
	case p.attrs && p.rawAttrs:
		res.attrs = info.xattrs
	case p.attrs:
		res.attrs = filterRGWAttrs(info.xattrs)
	}
	if p.meta {
		res.size, res.mtime = info.size, info.mtime
	}
	if p.objv != nil {
		res.version = info.version
		p.objv.read = info.version
	}
	return res, nil
}

// readMiss reads o from RADOS and caches what it read, a missing object as
// a negative entry; radosgw caches no other failure
// (svc_sys_obj_cache.cc:200-205).
func (s *sysobjs) readMiss(ctx context.Context, o sysObj, name string, p readParams) (cacheInfo, error) {
	pool, err := s.pools.get(ctx, o.pool)
	if err != nil {
		return cacheInfo{}, err
	}
	info, err := s.readRADOS(ctx, pool, o.oid, p)
	if err != nil {
		if errors.Is(err, radosclient.ErrNotFound) {
			s.cache.put(name, cacheInfo{status: -int32(syscall.ENOENT)})
		}
		return cacheInfo{}, fmt.Errorf("reading %s/%s: %w", o.pool, o.oid, err)
	}
	s.cache.put(name, info)
	return info, nil
}

// readRADOS reads oid in one op: the tracker's check and read, the data
// when p asks for it, the stat and every xattr. An object larger than the
// read's buffer is read again whole at the size the stat reported.
func (s *sysobjs) readRADOS(ctx context.Context, pool radosclient.Pool, oid string, p readParams) (cacheInfo, error) {
	size := uint64(sysobjReadSize)
	for range sysobjReadTries {
		rop := radosclient.NewReadOp()
		var ver *version.ReadResult
		if p.objv != nil {
			ver = p.objv.prepareRead(rop, s.release)
		}
		var data *radosclient.ReadResult
		if p.data {
			data = rop.Read(0, size)
		}
		stat := rop.Stat()
		xattrs := rop.GetXattrs()
		if _, err := pool.Read(ctx, oid, rop, radosclient.OpFlagNone); err != nil {
			return cacheInfo{}, err
		}
		if err := errors.Join(stat.Err, xattrs.Err); err != nil {
			return cacheInfo{}, err
		}
		info := cacheInfo{
			flags:  meta.CacheFlagXattrs | meta.CacheFlagMeta,
			xattrs: xattrs.Xattrs, size: stat.Size, mtime: stat.ModTime,
		}
		if data != nil {
			if data.Err != nil {
				return cacheInfo{}, data.Err
			}
			d := data.Data[:data.N]
			if stat.Size > uint64(len(d)) {
				size = stat.Size
				continue
			}
			info.flags |= meta.CacheFlagData
			info.data = d
		}
		if ver != nil {
			v, err := ver.Version()
			if err != nil {
				return cacheInfo{}, err
			}
			info.flags |= meta.CacheFlagObjVersion
			info.version = fromCls(v)
		}
		return info, nil
	}
	return cacheInfo{}, fmt.Errorf("object grew past each of %d reads", sysobjReadTries)
}

// filterRGWAttrs is rgw_filter_attrset with RGW_ATTR_PREFIX
// (driver/rados/rgw_tools.cc:285-296 at v19.2.6, :301-312 at v20.2.4): the
// user.rgw. xattrs alone.
func filterRGWAttrs(attrs map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(attrs))
	for k, v := range attrs {
		if strings.HasPrefix(k, meta.AttrPrefix) {
			out[k] = v
		}
	}
	return out
}

// write is RGWSI_SysObj_Cache::write over RGWSI_SysObj_Core::write
// (svc_sys_obj_cache.cc:312-354, svc_sys_obj_core.cc:477-542): an exclusive
// create, or a remove that may fail and a create, then v's version steps,
// the mtime, the data and each attr with a value, in one op. It returns the
// mtime stored, which is now when mtime is zero. A failed write invalidates
// the entry (see failed); a written one is cached and distributed.
func (s *sysobjs) write(ctx context.Context, o sysObj, data []byte, attrs map[string][]byte, exclusive bool, mtime time.Time, v *objv) (time.Time, error) {
	name := normalName(o.pool, o.oid)
	if mtime.IsZero() {
		mtime = s.now()
	}
	wop := radosclient.NewWriteOp()
	if exclusive {
		wop.Create(true)
	} else {
		wop.Remove()
		wop.SetStepFlags(radosclient.StepFlagFailOK)
		wop.Create(false)
	}
	if v != nil {
		v.prepareWrite(wop, s.release)
	}
	wop.SetMtime(mtime)
	wop.WriteFull(data)
	setXattrs(wop, attrs)
	if err := s.run(ctx, o, wop); err != nil {
		s.failed(ctx, o, name, err)
		return time.Time{}, fmt.Errorf("writing %s/%s: %w", o.pool, o.oid, err)
	}
	if v != nil {
		v.applyWrite()
	}
	info := cacheInfo{
		flags: meta.CacheFlagXattrs | meta.CacheFlagData | meta.CacheFlagMeta,
		data:  data, xattrs: attrs, size: uint64(len(data)), mtime: mtime,
	}
	if v != nil && v.read.Ver != 0 {
		info.flags |= meta.CacheFlagObjVersion
		info.version = v.read
	}
	s.update(ctx, o, name, info)
	return mtime, nil
}

// setAttrs is RGWSI_SysObj_Cache::set_attrs over RGWSI_SysObj_Core::set_attrs
// (svc_sys_obj_cache.cc:277-310, svc_sys_obj_core.cc:237-291): an exclusive
// create when asked, v's version steps, the removals, then each attr with a
// value, in one op that is not sent when it holds no step. A failed change
// invalidates the entry (see failed); an applied one is merged into it and
// distributed.
func (s *sysobjs) setAttrs(ctx context.Context, o sysObj, set map[string][]byte, rm []string, exclusive bool, v *objv) error {
	name := normalName(o.pool, o.oid)
	wop := radosclient.NewWriteOp()
	if exclusive {
		wop.Create(true)
	}
	if v != nil {
		v.prepareWrite(wop, s.release)
	}
	rmattrs := make(map[string][]byte, len(rm))
	for _, k := range rm {
		rmattrs[k] = nil
	}
	for _, k := range slices.Sorted(maps.Keys(rmattrs)) {
		wop.RmXattr(k)
	}
	setXattrs(wop, set)
	if len(wop.Steps()) > 0 {
		if err := s.run(ctx, o, wop); err != nil {
			s.failed(ctx, o, name, err)
			return fmt.Errorf("setting attrs of %s/%s: %w", o.pool, o.oid, err)
		}
		if v != nil {
			v.applyWrite()
		}
	}
	info := cacheInfo{flags: meta.CacheFlagModifyXattrs, xattrs: set, rmxattrs: rmattrs}
	if v != nil && v.read.Ver != 0 {
		info.flags |= meta.CacheFlagObjVersion
		info.version = v.read
	}
	s.update(ctx, o, name, info)
	return nil
}

// remove is RGWSI_SysObj_Cache::remove over RGWSI_SysObj_Core::remove
// (svc_sys_obj_cache.cc:86-111, svc_sys_obj_core.cc:451-475): v's version
// steps and the removal in one op; once it succeeds the entry is
// invalidated here and on every gateway. radosgw leaves the entry alone
// when the removal fails, and so does remove, unless the removal may have
// applied (see failed).
func (s *sysobjs) remove(ctx context.Context, o sysObj, v *objv) error {
	wop := radosclient.NewWriteOp()
	if v != nil {
		v.prepareWrite(wop, s.release)
	}
	wop.Remove()
	name := normalName(o.pool, o.oid)
	if err := s.run(ctx, o, wop); err != nil {
		if contextEnded(err) {
			s.failed(ctx, o, name, err)
		}
		return fmt.Errorf("removing %s/%s: %w", o.pool, o.oid, err)
	}
	s.invalidate(ctx, o, name)
	return nil
}

// run runs wop on o.
func (s *sysobjs) run(ctx context.Context, o sysObj, wop *radosclient.WriteOp) error {
	pool, err := s.pools.get(ctx, o.pool)
	if err != nil {
		return err
	}
	_, err = pool.Write(ctx, o.oid, wop, radosclient.OpFlagNone)
	return err
}

// setXattrs adds a SetXattr for each attr with a value, in name order as
// radosgw's map holds them; radosgw skips an empty one
// (svc_sys_obj_core.cc:518-526, :268-276).
func setXattrs(wop *radosclient.WriteOp, attrs map[string][]byte) {
	for _, k := range slices.Sorted(maps.Keys(attrs)) {
		if len(attrs[k]) > 0 {
			wop.SetXattr(k, attrs[k])
		}
	}
}

// update merges info into the local entry, then distributes it as an
// UPDATE_OBJ record, in radosgw's order (svc_sys_obj_cache.cc:301-302,
// :345-346), so a newer record announced meanwhile invalidates this one
// rather than being overwritten by it. This gateway's own watch receives
// the record too and, since a notify of either op invalidates here, drops
// the entry; the next lookup reads RADOS.
func (s *sysobjs) update(ctx context.Context, o sysObj, name string, info cacheInfo) {
	s.cache.put(name, info)
	s.distribute(ctx, o, name, meta.CacheNotifyInfo{
		Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: o.pool, OID: o.oid}, ObjInfo: info.wire(),
	})
}

// invalidate drops name's entry here and, with an INVALIDATE_OBJ record, on
// every gateway.
func (s *sysobjs) invalidate(ctx context.Context, o sysObj, name string) {
	s.cache.invalidateRemove(name)
	s.distribute(ctx, o, name, meta.CacheNotifyInfo{Op: meta.CacheInvalidateObj, Obj: meta.RawObj{Pool: o.pool, OID: o.oid}})
}

// failed handles a change that failed. radosgw invalidates the local entry
// (svc_sys_obj_cache.cc:305-307, :349-351). A change whose context ended may
// have applied all the same, since ending the context stops only the client
// (docs/cgo-limitations.md, "Cancellation stops only the client"), so every
// gateway is told to drop the entry too.
func (s *sysobjs) failed(ctx context.Context, o sysObj, name string, err error) {
	if contextEnded(err) {
		s.invalidate(ctx, o, name)
		return
	}
	s.cache.invalidateRemove(name)
}

// contextEnded reports whether err is a context's cancellation or deadline.
func contextEnded(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// distribute sends info to every gateway. A failure is logged and does not
// fail the change, as radosgw's is not (svc_sys_obj_cache.cc:105-108,
// :302-304, :346-348). The notify follows a change that has applied, or
// may have, so it does not end with the caller's context, which ends when
// a client disconnects; radosgw never cancels it. It is bounded by
// librados's notify timeout and rgw_max_notify_retries.
func (s *sysobjs) distribute(ctx context.Context, o sysObj, name string, info meta.CacheNotifyInfo) {
	if err := s.notify.distribute(context.WithoutCancel(ctx), name, info); err != nil {
		slog.ErrorContext(ctx, "distributing a cache notify",
			slog.String("pool", o.pool.String()), slog.String("oid", o.oid), slog.Any("error", err))
	}
}

// wire is the record a cache notify carries for i.
func (i cacheInfo) wire() meta.ObjectCacheInfo {
	return meta.ObjectCacheInfo{
		Status: i.status, Flags: i.flags, Data: i.data, Xattrs: i.xattrs, RMXattrs: i.rmxattrs,
		Meta: meta.ObjectMetaInfo{Size: i.size, Mtime: meta.Time{Time: i.mtime}}, Version: i.version,
	}
}
