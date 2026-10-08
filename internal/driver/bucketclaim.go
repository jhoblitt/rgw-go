package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A bucket claim is rgw-go's fence for the metadata section puts of a
// bucket: one object per bucket id, which names the bucket the id belongs
// to and, while a put of the bucket's entry point or instance is between
// its checks and its write, the owner that put writes. Each put reads the
// claim before what it checks, writes it under the version read before its
// own write, and clears the owner after, so of two puts that checked the
// same state the second fails. A new instance's marker is its id, so the
// claim of its id serializes the marker too. radosgw keeps no claim and
// writes none.

// bucketClaimPool is the pool of rgw-go's bucket claims: the domain root's
// pool, in a namespace of its own beside the domain root's, so a listing of
// the bucket sections never meets one.
func bucketClaimPool(root meta.Pool) meta.Pool {
	return meta.Pool{Name: root.Name, NS: root.NS + radosclient.BucketClaimNSSuffix}
}

// bucketClaimObj is the claim of bucket id.
func (s *Store) bucketClaimObj(id string) sysObj {
	return sysObj{pool: bucketClaimPool(s.zone.Params.DomainRoot), oid: id}
}

// bucketClaim is a claim's content: Bucket is the "tenant/name" or "name"
// the id belongs to, From the name a rename is moving the bucket from,
// empty when none is, and PendingOwner the owner a put of the bucket is
// writing, empty when none is.
type bucketClaim struct {
	Bucket       string `json:"bucket"`
	From         string `json:"from,omitempty"`
	PendingOwner string `json:"pending_owner,omitempty"`
}

// names reports whether the claim belongs to name: the bucket's, or, while
// a rename moves it, the name it moves from.
func (c bucketClaim) names(name string) bool {
	return c.Bucket == name || c.From != "" && c.From == name
}

// heldClaim is a claim as read or written, at version v.
type heldClaim struct {
	o     sysObj
	claim bucketClaim
	v     meta.ObjVersion
}

// readClaim is the claim of b.ID, nil when there is none. A claim naming
// another bucket than b's name, or a rename's other name, is
// ErrBucketAlreadyExists, as two names would reach one bucket's index and
// data.
func (s *Store) readClaim(ctx context.Context, b meta.BucketID) (*heldClaim, error) {
	h, err := s.fetchClaim(ctx, b.ID)
	if err != nil || h == nil {
		return h, err
	}
	if !h.claim.names(b.EntryPointOID()) {
		return nil, fmt.Errorf("bucket %s: its id is claimed by another bucket: %w", b.EntryPointOID(), op.ErrBucketAlreadyExists)
	}
	return h, nil
}

// fetchClaim is the claim of id, nil when there is none. One that does not
// decode or carries no version is ErrInternalError, as nothing could be
// written under it.
func (s *Store) fetchClaim(ctx context.Context, id string) (*heldClaim, error) {
	o := s.bucketClaimObj(id)
	v := &objv{}
	res, err := s.sysobj.read(ctx, o, readParams{data: true, objv: v})
	if err != nil {
		err = mapBucketErr(err)
		if errors.Is(err, op.ErrNoSuchBucket) {
			return nil, nil
		}
		return nil, err
	}
	h := &heldClaim{o: o, v: v.read}
	if err := json.Unmarshal(res.data, &h.claim); err != nil || h.v.Ver == 0 {
		return nil, fmt.Errorf("%w: %s does not decode or carries no version", op.ErrInternalError, s.sysobj.hidden.name(o.pool, o.oid))
	}
	return h, nil
}

// createClaim creates the claim of b.ID for b exclusively. A claim created
// since the caller read none fails it with radosclient.ErrExists.
func (s *Store) createClaim(ctx context.Context, b meta.BucketID) (*heldClaim, error) {
	h := &heldClaim{o: s.bucketClaimObj(b.ID), claim: bucketClaim{Bucket: b.EntryPointOID()}}
	data, err := json.Marshal(h.claim)
	if err != nil {
		return nil, err
	}
	v := &objv{write: newWriteVersion()}
	if _, err := s.sysobj.write(ctx, h.o, data, nil, true, time.Time{}, v); err != nil {
		return nil, err
	}
	h.v = v.read
	return h, nil
}

// claimBucket is readClaim, creating the claim when there is none; one
// another put created meanwhile is read again.
func (s *Store) claimBucket(ctx context.Context, b meta.BucketID) (*heldClaim, error) {
	for range 2 {
		h, err := s.readClaim(ctx, b)
		if err != nil || h != nil {
			return h, err
		}
		h, err = s.createClaim(ctx, b)
		if err == nil {
			return h, nil
		}
		if !errors.Is(err, radosclient.ErrExists) {
			return nil, mapBucketErr(err)
		}
		o := s.bucketClaimObj(b.ID)
		s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
	}
	return nil, fmt.Errorf("bucket %s: its claim keeps changing: %w", b.EntryPointOID(), op.ErrConcurrentModification)
}

// markClaim writes h under the version it was read at with owner pending,
// before the put's own write. Another put's pending owner other than owner
// is ErrConcurrentModification, as that put may not have written what this
// one checked, and so is a claim changed since it was read.
func (s *Store) markClaim(ctx context.Context, h *heldClaim, owner meta.Owner) error {
	if p := h.claim.PendingOwner; p != "" && p != owner.String() {
		return fmt.Errorf("bucket %s: a write naming another owner is in flight: %w", h.claim.Bucket, op.ErrConcurrentModification)
	}
	c := h.claim
	c.PendingOwner = owner.String()
	return s.writeClaim(ctx, h, c)
}

// clearClaim drops the pending owner markClaim wrote, under the version it
// wrote. A claim changed since is left: the put that changed it clears it.
func (s *Store) clearClaim(ctx context.Context, h *heldClaim) error {
	c := h.claim
	c.PendingOwner = ""
	err := s.writeClaim(ctx, h, c)
	if errors.Is(err, op.ErrConcurrentModification) {
		return nil
	}
	return err
}

// writeClaim writes c over h under h's version, leaving h at what it wrote.
func (s *Store) writeClaim(ctx context.Context, h *heldClaim, c bucketClaim) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	v := &objv{read: h.v}
	if _, err := s.sysobj.write(ctx, h.o, data, nil, false, time.Time{}, v); err != nil {
		if errors.Is(err, radosclient.ErrCanceled) || errors.Is(err, radosclient.ErrNotFound) {
			s.sysobj.cache.invalidateRemove(normalName(h.o.pool, h.o.oid))
			return fmt.Errorf("bucket %s: its claim changed: %w", h.claim.Bucket, op.ErrConcurrentModification)
		}
		return mapBucketErr(err)
	}
	h.claim, h.v = c, v.read
	return nil
}

// moveClaim points the claim of the bucket id a rename moves from src to
// dst, before the rename writes anything under dst: a claim naming src, or
// a rename's earlier move from src, is written naming dst, with src as its
// From, under the version read; a missing one is created so, exclusively,
// as a metadata put may create one meanwhile; one already naming dst is a
// retry's. Until settleClaim drops From, a put of either name finds the
// claim its own, so a rename that stops anywhere blocks neither name's
// puts and its retry finds the claim moved. A claim naming a third bucket
// is ErrBucketAlreadyExists. A put's pending owner is kept, but the
// rewrite fails that put's clear, which leaves its mark for a put of the
// marked owner to clear.
func (s *Store) moveClaim(ctx context.Context, src, dst meta.BucketID) error {
	srcName, dstName := src.EntryPointOID(), dst.EntryPointOID()
	for range 2 {
		h, err := s.fetchClaim(ctx, src.ID)
		if err != nil {
			return err
		}
		if h == nil {
			c := bucketClaim{Bucket: dstName, From: srcName}
			data, err := json.Marshal(c)
			if err != nil {
				return err
			}
			o := s.bucketClaimObj(src.ID)
			_, err = s.sysobj.write(ctx, o, data, nil, true, time.Time{}, &objv{write: newWriteVersion()})
			if err == nil {
				return nil
			}
			if !errors.Is(err, radosclient.ErrExists) {
				return mapBucketErr(err)
			}
			s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
			continue
		}
		switch {
		case h.claim.Bucket == dstName:
			return nil
		case h.claim.names(srcName):
			c := h.claim
			c.Bucket, c.From = dstName, srcName
			return s.writeClaim(ctx, h, c)
		}
		return fmt.Errorf("bucket %s: its id is claimed by another bucket: %w", srcName, op.ErrBucketAlreadyExists)
	}
	return fmt.Errorf("bucket %s: its claim keeps changing: %w", srcName, op.ErrConcurrentModification)
}

// settleClaim drops the From moveClaim wrote once the rename to dst is
// done, best effort: a failure is logged and leaves the old name a put may
// still find the claim for, which no live bucket answers to. A claim a put
// has marked is left as it is, old name and all, as a write of it would
// fail that put's clear and leave its mark.
func (s *Store) settleClaim(ctx context.Context, dst meta.BucketID) {
	h, err := s.fetchClaim(ctx, dst.ID)
	if err == nil && h != nil && h.claim.Bucket == dst.EntryPointOID() && h.claim.From != "" && h.claim.PendingOwner == "" {
		c := h.claim
		c.From = ""
		err = s.writeClaim(ctx, h, c)
	}
	if err != nil {
		o := s.bucketClaimObj(dst.ID)
		slog.WarnContext(ctx, "leaving a renamed bucket's claim naming its old name",
			slog.String("pool", o.pool.String()), s.sysobj.hidden.attr(o.pool, o.oid), slog.String("bucket", dst.Name), slog.Any("error", err))
	}
}

// abandonTimeout bounds abandonMark's clear.
const abandonTimeout = 10 * time.Second

// abandonMark clears h's mark after the put's own write failed at err,
// best effort, when the failure says the write did not apply: a lost
// version race or exclusive create. Any other failure leaves the mark, as a
// stopped put does, for a put of the marked owner to clear: the put's
// context ending among them, as a write may still land after its context
// ends (internal/radosclient/doc.go), and a mark cleared meanwhile would
// let a put of another owner land beside it. A failed clear is logged.
func (s *Store) abandonMark(ctx context.Context, h *heldClaim, err error) {
	definite := errors.Is(err, radosclient.ErrCanceled) || errors.Is(err, radosclient.ErrExists) ||
		errors.Is(err, op.ErrConcurrentModification) || errors.Is(err, op.ErrBucketAlreadyExists)
	if !definite || contextEnded(err) {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abandonTimeout)
	defer cancel()
	if cerr := s.clearClaim(cctx, h); cerr != nil {
		slog.WarnContext(ctx, "leaving a failed put's mark on its bucket claim",
			slog.String("pool", h.o.pool.String()), s.sysobj.hidden.attr(h.o.pool, h.o.oid), slog.Any("error", cerr))
	}
}
