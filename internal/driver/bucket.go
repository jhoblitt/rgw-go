package driver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A bucket is two objects in the zone's domain_root: its entry point, named
// by tenant and name, which names the bucket's current instance, and that
// instance, which holds the RGWBucketInfo and the bucket's attrs. The store
// is RGWBucketCtl (driver/rados/rgw_bucket.cc) over RGWSI_Bucket_SObj
// (services/svc_bucket_sobj.cc). A bare line number is v19.2.6's, and each
// fact cited holds at v20.2.4 too. radosgw also logs each write and removal
// of these objects in the metadata log, which it keeps only for multisite
// (docs/exclusions.md, "Multisite").

// epObj is a bucket's entry point object, named by get_entrypoint_meta_key
// (services/svc_bucket.cc:9-19 at both tags).
func (s *Store) epObj(tenant, name string) sysObj {
	return sysObj{pool: s.zone.Params.DomainRoot, oid: meta.BucketID{Tenant: tenant, Name: name}.EntryPointOID()}
}

// instanceObj is a bucket's instance object, named by get_bi_meta_key
// (svc_bucket.cc:21-24) as the instance module turns a key into an oid
// (svc_bucket_sobj.cc:88-98; v20.2.4 :28-39).
func (s *Store) instanceObj(b meta.BucketID) sysObj {
	return sysObj{pool: s.zone.Params.DomainRoot, oid: b.InstanceOID()}
}

// entryPoint is what read_bucket_entrypoint_info returns.
type entryPoint struct {
	ep    meta.BucketEntryPoint
	attrs map[string][]byte
	mtime time.Time
	v     objv
}

// instance is what do_read_bucket_instance_info returns.
type instance struct {
	info  meta.BucketInfo
	attrs map[string][]byte
	mtime time.Time
	v     objv
}

// mapBucketErr maps a seam error from a bucket's objects as op.FromRADOS
// does at bucket scope: ENOENT is NoSuchBucket, EEXIST BucketAlreadyExists
// and ECANCELED, a lost version race, ConcurrentModification.
func mapBucketErr(err error) error { return op.FromRADOS(err, op.ScopeBucket) }

// readEntryPoint is read_bucket_entrypoint_info (svc_bucket_sobj.cc:208-237;
// v20.2.4 :159-186). An entry point that does not decode is radosgw's -EIO,
// which no row of the S3 error table names.
func (s *Store) readEntryPoint(ctx context.Context, tenant, name string) (*entryPoint, error) {
	e := &entryPoint{}
	o := s.epObj(tenant, name)
	res, err := s.sysobj.read(ctx, o, readParams{data: true, attrs: true, meta: true, objv: &e.v})
	if err != nil {
		return nil, mapBucketErr(err)
	}
	d := denc.NewDecoder(res.data)
	e.ep = meta.DecodeBucketEntryPoint(d)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("%w: decoding bucket entry point %s: %w", op.ErrUnknown, o.oid, err)
	}
	e.attrs, e.mtime = res.attrs, res.mtime
	return e, nil
}

// writeEntryPoint is store_bucket_entrypoint_info (svc_bucket_sobj.cc:239-260;
// v20.2.4 :188-208), ep encoded at the cluster's release.
func (s *Store) writeEntryPoint(ctx context.Context, ep meta.BucketEntryPoint, attrs map[string][]byte, exclusive bool, mtime time.Time, v *objv) error {
	_, err := s.sysobj.write(ctx, s.epObj(ep.Bucket.Tenant, ep.Bucket.Name), encodeAt(ep, s.release), attrs, exclusive, mtime, v)
	return mapBucketErr(err)
}

// removeEntryPoint is remove_bucket_entrypoint_info (svc_bucket_sobj.cc:262-270;
// v20.2.4 :210-222).
func (s *Store) removeEntryPoint(ctx context.Context, tenant, name string, v *objv) error {
	return mapBucketErr(s.sysobj.remove(ctx, s.epObj(tenant, name), v))
}

// readInstance is do_read_bucket_instance_info (svc_bucket_sobj.cc:343-372;
// v20.2.4 :293-321). An instance that does not decode is radosgw's -EIO.
func (s *Store) readInstance(ctx context.Context, b meta.BucketID) (*instance, error) {
	in := &instance{}
	o := s.instanceObj(b)
	res, err := s.sysobj.read(ctx, o, readParams{data: true, attrs: true, meta: true, objv: &in.v})
	if err != nil {
		return nil, mapBucketErr(err)
	}
	d := denc.NewDecoder(res.data)
	in.info = meta.DecodeBucketInfo(d)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("%w: decoding bucket instance %s: %w", op.ErrUnknown, o.oid, err)
	}
	in.attrs, in.mtime = res.attrs, res.mtime
	return in, nil
}

// writeInstance is store_bucket_instance_info's write
// (svc_bucket_sobj.cc:537-558; v20.2.4 :473-498), info encoded at the
// cluster's release. An exclusive write that finds the instance there
// succeeds without writing, as radosgw's does: an instance's name is unique
// to it, so the one there is the one being written.
func (s *Store) writeInstance(ctx context.Context, info *meta.BucketInfo, attrs map[string][]byte, exclusive bool, mtime time.Time, v *objv) error {
	_, err := s.sysobj.write(ctx, s.instanceObj(info.Bucket), encodeAt(*info, s.release), attrs, exclusive, mtime, v)
	if errors.Is(err, radosclient.ErrExists) {
		return nil
	}
	return mapBucketErr(err)
}

// removeInstance is remove_bucket_instance_info (svc_bucket_sobj.cc:567-591;
// v20.2.4 :507-530): an instance already gone is no failure.
func (s *Store) removeInstance(ctx context.Context, b meta.BucketID, v *objv) error {
	err := s.sysobj.remove(ctx, s.instanceObj(b), v)
	if errors.Is(err, radosclient.ErrNotFound) {
		return nil
	}
	return mapBucketErr(err)
}

// GetBucket implements op.BucketStore as RadosBucket::load_bucket by name
// (driver/rados/rgw_sal_rados.cc:610-637; v20.2.4 :631-655), which reads
// through RGWBucketCtl::read_bucket_info (rgw_bucket.cc:3085-3129; v20.2.4
// :3234-3273): the entry point, then the instance it names, whose attrs,
// mtime and version the record carries. An entry point from before version
// 8 holds the bucket info itself and names no instance; load_bucket never
// looks at that info and reads the instance of the empty bucket, which
// does not exist, and so does GetBucket (docs/ceph-upstream-bugs.md,
// "radosgw cannot load a bucket whose entry point is from before version
// 8").
func (s *Store) GetBucket(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
	e, err := s.readEntryPoint(ctx, tenant, name)
	if err != nil {
		return nil, err
	}
	in, err := s.readInstance(ctx, e.ep.Bucket)
	if err != nil {
		return nil, err
	}
	return &op.BucketRecord{EntryPoint: e.ep, Info: in.info, Attrs: in.attrs, Version: in.v.read, EPVersion: e.v.read, Mtime: in.mtime}, nil
}

// GetBucketInstance implements op.BucketStore as load_bucket with a bucket
// id (rgw_sal_rados.cc:623-629; v20.2.4 :642-647), which reads the instance
// alone; the record's entry point holds only the bucket.
func (s *Store) GetBucketInstance(ctx context.Context, id meta.BucketID) (*op.BucketRecord, error) {
	in, err := s.readInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	ep := meta.NewBucketEntryPoint()
	ep.Bucket = in.info.Bucket
	return &op.BucketRecord{EntryPoint: ep, Info: in.info, Attrs: in.attrs, Version: in.v.read, Mtime: in.mtime}, nil
}

// PutBucketInfo implements op.BucketStore as RadosBucket::put_info
// (rgw_sal_rados.cc:753-757; v20.2.4 :771-775) through
// store_bucket_instance_info (svc_bucket_sobj.cc:489-565; v20.2.4 :430-505):
// the instance rewritten whole, under rec.Version, with rec.Attrs, at the
// time now. handle_overwrite and handle_bi_update, which keep multisite's
// bucket index log and sync hints, are not run (docs/exclusions.md,
// "Multisite").
func (s *Store) PutBucketInfo(ctx context.Context, rec *op.BucketRecord) error {
	v := &objv{read: rec.Version}
	mtime := s.sysobj.now()
	if err := s.writeInstance(ctx, &rec.Info, rec.Attrs, false, mtime, v); err != nil {
		return err
	}
	rec.Version, rec.Mtime = v.read, mtime
	return nil
}

// PutBucketAttrs implements op.BucketStore: rec.Attrs with set laid over
// them, then rm removed, written with the instance as PutBucketInfo writes
// it. radosgw sets attrs through RadosBucket::merge_and_store_attrs
// (rgw_sal_rados.cc:771-778; v20.2.4 :789-796) and removes them by erasing
// them from the bucket's attrs and calling put_info, as DeleteBucketTagging
// and DeleteBucketPolicy do (rgw_op.cc:1232-1235 and :8204-8207; v20.2.4
// :1469-1472 and :9151-9155).
//
// merge_and_store_attrs goes through set_bucket_instance_attrs
// (rgw_bucket.cc:3277-3304; v20.2.4 :3397-3421), which, for an instance
// without has_instance_obj, first reads the entry point, to convert one from
// before version 8 (convert_old_bucket_info, :3237-3275; v20.2.4
// :3359-3395). radosgw has not set has_instance_obj since Octopus, so no
// bucket created since carries it. A PutBucketAttrs that sets attrs reads
// the entry point too, so it fails as that read fails, and refuses a bucket
// that needs the conversion (docs/exclusions.md, "An entry point from before
// version 8 is not converted"); one that only removes them reads none, as
// put_info reads none.
func (s *Store) PutBucketAttrs(ctx context.Context, rec *op.BucketRecord, set map[string][]byte, rm []string) error {
	if len(set) > 0 && !rec.Info.HasInstanceObj {
		b := rec.Info.Bucket
		e, err := s.readEntryPoint(ctx, b.Tenant, b.Name)
		if err != nil {
			return err
		}
		if e.ep.HasBucketInfo {
			return fmt.Errorf("%w: bucket %s has an entry point from before version 8, which is not converted", op.ErrInternalError, b.EntryPointOID())
		}
	}
	attrs := maps.Clone(rec.Attrs)
	if attrs == nil {
		attrs = make(map[string][]byte, len(set))
	}
	maps.Copy(attrs, set)
	for _, k := range rm {
		delete(attrs, k)
	}
	saved := rec.Attrs
	rec.Attrs = attrs
	if err := s.PutBucketInfo(ctx, rec); err != nil {
		rec.Attrs = saved
		return err
	}
	return nil
}
