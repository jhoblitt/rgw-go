package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// sectionBucket names the section of RGWBucketMetadataHandler
// (driver/rados/rgw_bucket.cc), the bucket entry points in the zone's domain
// root (services/svc_bucket_sobj.cc).
const sectionBucket = "bucket"

type bucketSection struct{}

func (bucketSection) get(ctx context.Context, s *Store, key string) (op.MetadataEntry, error) {
	return s.getEntryPoint(ctx, key)
}

func (bucketSection) put(ctx context.Context, s *Store, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
	return s.putEntryPoint(ctx, key, e, opts)
}

func (bucketSection) remove(ctx context.Context, s *Store, key string) error {
	return s.removeEntryPointEntry(ctx, key)
}

func (bucketSection) pool(s *Store) meta.Pool { return s.zone.Params.DomainRoot }

// oidKey is RGWSI_Bucket_SObj_Module::is_valid_oid
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L45-L47;
// BucketEntrypointLister, v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bucket_sobj.cc#L95-L112):
// not behind a '.'.
func (bucketSection) oidKey(oid string) (string, bool) {
	return oid, oid != "" && oid[0] != '.'
}

// entryPointKey splits a bucket section key, "tenant/name" or "name", the
// entry point's oid
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L49-L57).
// A key whose split names another oid, as an empty tenant before a '/'
// would, names no entry point.
func entryPointKey(key string) (tenant, name string, ok bool) {
	if t, n, found := strings.Cut(key, "/"); found {
		tenant, name = t, n
	} else {
		name = key
	}
	return tenant, name, meta.BucketID{Tenant: tenant, Name: name}.EntryPointOID() == key && name != ""
}

// getEntryPoint is RGWBucketMetadataHandler::do_get
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2158-L2176;
// get, v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L2336-L2353).
func (s *Store) getEntryPoint(ctx context.Context, key string) (op.MetadataEntry, error) {
	tenant, name, ok := entryPointKey(key)
	if !ok {
		return op.MetadataEntry{}, fmt.Errorf("bucket %q: %w", key, op.ErrNoSuchKey)
	}
	e, err := s.readEntryPoint(ctx, tenant, name)
	if err != nil {
		return op.MetadataEntry{}, noSuchKey(err)
	}
	data, err := json.Marshal(e.ep)
	if err != nil {
		return op.MetadataEntry{}, err
	}
	return op.MetadataEntry{Key: key, Data: data, Doc: e.ep, Version: e.v.read, Mtime: e.mtime}, nil
}

// putEntryPoint is RGWMetadataHandlerPut_Bucket's put_checked and put_post
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2265-L2317;
// v20.2.4 RGWBucketMetadataHandler::put,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L2355-L2401).
// The entry point is written with the old entry point's attrs, and the
// owner lists follow the entry point written: its owner's list names the
// bucket while it is linked and not while it is unlinked, and an old
// owner's list entry naming the bucket goes. The new owner's list is set
// from the entry point after it is written, so a put that stops between is
// completed there by its retry; the old owner's entry is not, as the retry
// reads the new entry point as the old one, and stays in that list.
//
// The bucket's claim (bucketclaim.go) is read before the instance is
// checked and, for a claim that does not exist yet, created after; it is
// marked with the entry point's owner before the entry point is written and
// cleared after, so an instance put changing the owner meanwhile fails one
// of the two.
//
// Each refusal comes before anything is written. radosgw writes the entry
// point the document gives under the key, whatever bucket, instance and
// owner it names; rgw-go refuses:
//   - a document naming another bucket than the key, or one from before
//     version 8, which embeds its bucket info, and a NUL byte in the
//     bucket id, marker or owner it names: ErrInvalidArgument;
//   - over a stored entry point, another bucket id: ErrBucketAlreadyExists,
//     as the name is held by the bucket it names, which would be left
//     without it;
//   - an instance the document names that does not exist, and an owner
//     other than that instance's: ErrInvalidArgument, as an entry point
//     owner apart from the instance's is a second owner to every later
//     link, unlink and chown;
//   - for a new entry point, an instance whose id another entry point
//     loads: ErrBucketAlreadyExists, as two names would reach one bucket's
//     index and data;
//   - a bucket a live rename or a removal holds, as the bucket admin store
//     refuses its other writes.
func (s *Store) putEntryPoint(ctx context.Context, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
	var ep meta.BucketEntryPoint
	if err := decodeDoc(e.Data, &ep); err != nil {
		return err
	}
	tenant, name, ok := entryPointKey(key)
	if !ok || ep.Bucket.Tenant != tenant || ep.Bucket.Name != name {
		return fmt.Errorf("%w: the document names bucket %s under key %s", op.ErrInvalidArgument, ep.Bucket.Key(), key)
	}
	if ep.HasBucketInfo {
		return fmt.Errorf("%w: an entry point from before version 8 is not written", op.ErrInvalidArgument)
	}
	if err := op.RefuseNUL("the entry point", ep.Bucket.ID, ep.Bucket.Marker, ep.Owner.String()); err != nil {
		return err
	}
	old, err := s.readEntryPoint(ctx, tenant, name)
	switch {
	case errors.Is(err, op.ErrNoSuchBucket):
		old = nil
	case err != nil:
		return err
	}
	var read meta.ObjVersion
	if old != nil {
		read = old.v.read
	}
	err = guardVersion("bucket "+key, old != nil, read, opts)
	if err != nil {
		return err
	}
	if old != nil && old.ep.Bucket.ID != ep.Bucket.ID {
		return fmt.Errorf("bucket %s names instance %s: %w", key, old.ep.Bucket.ID, op.ErrBucketAlreadyExists)
	}
	claim, err := s.readClaim(ctx, ep.Bucket)
	if err != nil {
		return err
	}
	in, err := s.readInstance(ctx, ep.Bucket)
	if errors.Is(err, op.ErrNoSuchBucket) {
		return fmt.Errorf("%w: bucket %s: the instance %s does not exist", op.ErrInvalidArgument, key, ep.Bucket.ID)
	}
	if err != nil {
		return err
	}
	err = s.busyBucket(ctx, in.info, in.attrs)
	if err != nil {
		return err
	}
	if ep.Owner.String() != in.info.Owner.String() {
		return fmt.Errorf("%w: bucket %s: the entry point's owner is not its instance's", op.ErrInvalidArgument, key)
	}
	if old == nil {
		err = s.refuseNamedElsewhere(ctx, ep.Bucket)
		if err != nil {
			return err
		}
	}
	if claim == nil {
		// The instance was read before the claim existed, so a claim
		// another put creates meanwhile may follow a write this did not see.
		claim, err = s.createClaim(ctx, ep.Bucket)
		if errors.Is(err, radosclient.ErrExists) {
			return fmt.Errorf("bucket %s: %w", key, op.ErrConcurrentModification)
		}
		if err != nil {
			return mapBucketErr(err)
		}
	}
	err = s.markClaim(ctx, claim, ep.Owner)
	if err != nil {
		return err
	}
	var attrs map[string][]byte
	if old != nil {
		attrs = old.attrs
	}
	err = s.writeEntryPoint(ctx, ep, attrs, old == nil, e.Mtime, writeVersion(read, old != nil, e))
	if err != nil {
		s.abandonMark(ctx, claim, err)
		return err
	}
	err = s.clearClaim(ctx, claim)
	if err != nil {
		return err
	}
	if old != nil && old.ep.Owner.String() != ep.Owner.String() {
		err = s.unlinkInstance(ctx, old.ep.Owner, ep.Bucket)
		if err != nil {
			return op.FromRADOS(err, op.ScopeBucket)
		}
	}
	if ep.Linked {
		err = s.linkBucket(ctx, ep.Owner, ep.Bucket, ep.CreationTime.Time)
	} else {
		err = s.unlinkInstance(ctx, ep.Owner, ep.Bucket)
	}
	return op.FromRADOS(err, op.ScopeBucket)
}

// refuseNamedElsewhere is ErrBucketAlreadyExists when an entry point under
// another name than b's loads b's id, read from every page of the bucket
// section's listing. One that cannot be read refuses too, as what it names
// cannot be told; one gone since the listing is skipped.
func (s *Store) refuseNamedElsewhere(ctx context.Context, b meta.BucketID) error {
	marker := ""
	for {
		keys, next, more, err := s.List(ctx, sectionBucket, marker, listPage)
		if err != nil {
			return err
		}
		for _, k := range keys {
			tenant, name, _ := entryPointKey(k)
			if tenant == b.Tenant && name == b.Name {
				continue
			}
			e, err := s.readEntryPoint(ctx, tenant, name)
			switch {
			case errors.Is(err, op.ErrNoSuchBucket):
				continue
			case err != nil:
				return err
			case e.ep.Bucket.ID == b.ID:
				return fmt.Errorf("bucket %s loads instance %s: %w", k, b.ID, op.ErrBucketAlreadyExists)
			}
		}
		if !more {
			return nil
		}
		marker = next
	}
}

// removeEntryPointEntry is RGWBucketMetadataHandler::do_remove
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2185-L2213;
// remove, v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L2489-L2511),
// which unlinks the bucket from its owner and removes its entry point, logging
// either failure and answering success. rgw-go removes only an entry point whose
// instance is gone, a name that loads nothing: radosgw's removal of a live
// bucket's entry point leaves the bucket without its name, its index and objects
// reachable by no request, which is ErrConcurrentModification here. The
// owner's list entry naming the instance goes first, so a removal that
// stops between is completed by its retry, then the entry point, through
// RemoveDanglingEntryPoint, under the version read; each failure is
// returned.
func (s *Store) removeEntryPointEntry(ctx context.Context, key string) error {
	tenant, name, ok := entryPointKey(key)
	if !ok {
		return fmt.Errorf("bucket %q: %w", key, op.ErrNoSuchKey)
	}
	e, err := s.readEntryPoint(ctx, tenant, name)
	if err != nil {
		return noSuchKey(err)
	}
	_, err = s.readInstance(ctx, e.ep.Bucket)
	switch {
	case err == nil:
		return fmt.Errorf("bucket %s: its instance exists, so it is removed with the bucket: %w", key, op.ErrConcurrentModification)
	case !errors.Is(err, op.ErrNoSuchBucket):
		return err
	}
	if err := s.unlinkInstance(ctx, e.ep.Owner, e.ep.Bucket); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}
	return noSuchKey(s.RemoveDanglingEntryPoint(ctx, tenant, name))
}
