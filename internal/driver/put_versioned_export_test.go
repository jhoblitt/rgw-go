package driver

import (
	"context"
	"io"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// PutObjectAtEpochForTest is PutObject linking a versioned write at epoch,
// for the external specs.
func (s *Store) PutObjectAtEpochForTest(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, body io.Reader, p op.PutParams, epoch uint64) (*op.PutResult, error) {
	return s.putObject(ctx, rec, key, body, p, epoch)
}

// SetOLHInitedForTest is setOLH handed opTag, a modification already inited
// on head, the OLH head as that init left it, for the external specs.
func (s *Store) SetOLHInitedForTest(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p OLHLink, head *op.ObjectState, opTag string) error {
	p.Inited = &olhInited{head: head, opTag: opTag}
	return s.setOLH(ctx, rec, key, p)
}

// ObserveHeadAttrsForTest makes see take every attrs-finalizing call of s's
// head writes, until restore.
func ObserveHeadAttrsForTest(s *Store, see func(rec *op.BucketRecord, mtime time.Time, create bool)) (restore func()) {
	headAttrsSeen = func(st *Store, rec *op.BucketRecord, mtime time.Time, create bool) {
		if st == s {
			see(rec, mtime, create)
		}
	}
	return func() { headAttrsSeen = nil }
}
