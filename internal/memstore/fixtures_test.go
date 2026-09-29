package memstore_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// start is the fixed time every spec's clock begins at.
var start = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

// clock is a spec's settable time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// newStore returns a store with the default zone on a clock reading start.
func newStore() (*memstore.Store, *clock) {
	c := &clock{t: start}
	return memstore.New(memstore.Config{Now: c.now}), c
}

func owner(id string) meta.Owner { return meta.UserOwner(meta.UserID{ID: id}) }

// tagPattern is RGWObjVersionTracker::generate_new_write_ver's tag: 24
// characters of gen_rand_alphanumeric's table.
const tagPattern = `^[A-Za-z0-9_-]{24}$`

func mustCreate(ctx context.Context, s *memstore.Store, tenant, name string, o meta.Owner) *op.BucketRecord {
	GinkgoHelper()
	rec, err := s.CreateBucket(ctx, op.CreateBucketParams{Tenant: tenant, Name: name, Owner: o, Placement: meta.PlacementRule{Name: "default-placement"}})
	Expect(err).NotTo(HaveOccurred())
	return rec
}

func mustPut(ctx context.Context, s *memstore.Store, rec *op.BucketRecord, key, body string) *op.PutResult {
	GinkgoHelper()
	res, err := s.PutObject(ctx, rec, meta.ObjKey{Name: key}, strings.NewReader(body), op.PutParams{Size: int64(len(body))})
	Expect(err).NotTo(HaveOccurred())
	return res
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}
