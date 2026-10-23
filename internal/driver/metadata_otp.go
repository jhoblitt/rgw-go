package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// sectionOTP names the section of radosgw's otp handler, the users' MFA
// objects in the zone's otp_pool (driver/rados/rgw_otp.cc). A key is the
// object's name, otp.OTPObject's "user:<uid>" for a user's devices, with no
// prefix
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_otp.cc#L15-L47 at v19.2.7;
// the lister without a prefix,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L110-L138 at v20.2.4),
// and the metadata key "otp:" and the key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_meta_be_otp.cc#L20-L23 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L145-L148 at v20.2.4).
// The document is op.OTPDocument.
//
// radosgw reads and writes the object with librados alone, through no
// cache, and so does this section's get and put; its removal goes through
// the cache, which tells every gateway to drop the object, as radosgw's
// does. A put changes no user record: a user's mfa_ids are the user
// section's.
const sectionOTP = "otp"

type otpSection struct{}

func (otpSection) get(ctx context.Context, s *Store, key string) (op.MetadataEntry, error) {
	doc, ver, mtime, err := s.readOTP(ctx, key)
	if err != nil {
		return op.MetadataEntry{}, err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return op.MetadataEntry{}, err
	}
	return op.MetadataEntry{Key: key, Data: data, Doc: doc, Version: ver, Mtime: mtime}, nil
}

func (otpSection) put(ctx context.Context, s *Store, key string, e op.MetadataEntry, _ op.PutMetadataOptions) error {
	return s.putOTP(ctx, key, e)
}

func (otpSection) remove(ctx context.Context, s *Store, key string) error {
	return s.removeOTP(ctx, key)
}

func (otpSection) pool(s *Store) meta.Pool { return s.zone.Params.OTPPool }

// oidKey is RGW_MB_Handler_Module_OTP's is_valid_oid and oid_to_key: every
// object, keyed by its name
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_otp.cc#L36-L46 at v19.2.7).
func (otpSection) oidKey(oid string) (string, bool) { return oid, true }

// readOTP is RGWSI_Cls::MFA::list_mfa
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L218-L245 at v19.2.7 and v20.2.4):
// one read op of a stat for the mtime, the version and otp_get's get_all.
// The document's devices carry no seed_bin, which otp_info_t::dump writes
// none of. A missing object is op.ErrNoSuchKey, and so is one whose header
// names a device without a record, which fails get_all.
func (s *Store) readOTP(ctx context.Context, key string) (op.OTPDocument, meta.ObjVersion, time.Time, error) {
	pool, err := s.pools.get(ctx, s.zone.Params.OTPPool)
	if err != nil {
		return op.OTPDocument{}, meta.ObjVersion{}, time.Time{}, fmt.Errorf("otp %s: %w", key, err)
	}
	r := radosclient.NewReadOp()
	stat := r.Stat()
	ver := (&objv{}).prepareRead(r, s.release)
	devs := otp.GetAll(r, s.release)
	if _, err = pool.Read(ctx, key, r, radosclient.OpFlagNone); err != nil {
		return op.OTPDocument{}, meta.ObjVersion{}, time.Time{}, otpErr(key, err)
	}
	v, err := ver.Version()
	if err != nil {
		return op.OTPDocument{}, meta.ObjVersion{}, time.Time{}, otpErr(key, err)
	}
	list, err := devs.Devices()
	if err != nil {
		return op.OTPDocument{}, meta.ObjVersion{}, time.Time{}, otpErr(key, err)
	}
	if stat.Err != nil {
		return op.OTPDocument{}, meta.ObjVersion{}, time.Time{}, otpErr(key, stat.Err)
	}
	doc := op.OTPDocument{Devices: make([]otp.OTPInfo, 0, len(list))}
	for _, d := range list {
		d.SeedBin = nil
		doc.Devices = append(doc.Devices, d)
	}
	return doc, fromCls(v), stat.ModTime, nil
}

// putOTP is the otp handler's put, RGWSI_Cls::MFA::set_mfa with reset_obj
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L188-L216 at v19.2.7 and v20.2.4),
// once op.DecodeOTPDocument has taken the document. It is one write op: a
// remove allowed to fail and a non-exclusive create; prepare_mfa_write's
// version, e's when its tag is set and otherwise a fresh one, with the
// mtime e names
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L65-L87 at v19.2.7 and v20.2.4);
// otp_set with the devices; then the header, holding exactly the
// document's ids.
//
// The OSD serves a class call's omap reads from the stored object, not the
// op's earlier steps
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7779-L7800 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7856-L7877 at v20.2.4),
// so otp_set keeps the replay state of each device the document keeps, as
// radosgw's put does, and writes a header listing the stored ids beside the
// document's. The header written after it drops the ids of the devices the
// document drops, whose records the remove dropped, where radosgw's put
// keeps them and every later get_all of the object fails
// (docs/ceph-upstream-bugs.md, "radosgw's otp metadata put keeps a dropped
// device's id in the otp header"). The put reads nothing first, as radosgw's
// does, so it takes no version guard.
func (s *Store) putOTP(ctx context.Context, key string, e op.MetadataEntry) error {
	doc, err := op.DecodeOTPDocument(e.Data)
	if err != nil {
		return err
	}
	pool, err := s.pools.get(ctx, s.zone.Params.OTPPool)
	if err != nil {
		return fmt.Errorf("otp %s: %w", key, err)
	}
	ids := make([]string, 0, len(doc.Devices))
	for _, d := range doc.Devices {
		ids = append(ids, d.ID)
	}
	w := radosclient.NewWriteOp()
	w.Remove()
	w.SetStepFlags(radosclient.StepFlagFailOK)
	w.Create(false)
	v := &objv{write: e.Version}
	if e.Version.Tag == "" {
		v.write = newWriteVersion()
	}
	v.prepareWrite(w, s.release)
	w.SetMtime(e.Mtime)
	otp.Set(w, doc.Devices, s.release)
	w.OmapSet(map[string][]byte{otp.HeaderKey: encodeAt(otp.Header{IDs: ids}, s.release)})
	if _, err := pool.Write(ctx, key, w, radosclient.OpFlagNone); err != nil {
		return otpErr(key, err)
	}
	return nil
}

// removeOTP is RGWMetadataManager::remove of an otp entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_metadata.cc#L554-L573 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_metadata.cc#L349-L369 at v20.2.4):
// the entry read, then the object removed through the cache under the
// version read, as rgw_delete_system_obj removes it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L84-L92 at v20.2.4;
// RGWSI_MetaBackend_SObj::remove_entry,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_meta_be_sobj.cc#L167-L184 at v19.2.7).
func (s *Store) removeOTP(ctx context.Context, key string) error {
	_, ver, _, err := s.readOTP(ctx, key)
	if err != nil {
		return err
	}
	err = s.sysobj.remove(ctx, sysObj{pool: s.zone.Params.OTPPool, oid: key}, &objv{read: ver})
	if err != nil {
		return otpErr(key, err)
	}
	return nil
}

// otpErr maps a failure of an op on the otp object key: a missing object,
// or a device its header names without a record, is op.ErrNoSuchKey, and
// any other errno radosgw's row for it.
func otpErr(key string, err error) error {
	if errors.Is(err, radosclient.ErrNotFound) {
		return fmt.Errorf("otp %s: %w: %w", key, op.ErrNoSuchKey, err)
	}
	return fmt.Errorf("otp %s: %w", key, op.FromRADOS(err, op.ScopeService))
}
