package goceph

import (
	"context"

	"github.com/ceph/go-ceph/rados"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// ListObjectsFrom implements radosclient.Pool with radosclient.ListPage over
// a librados listing. ListPage's seek to the last object's placement hash
// is not exact: rados_nobjects_list_seek leaves a fresh listing's
// sort_bitwise false, so its first fetch takes the cluster's bitwise sort as
// a change of order and restarts at the start of the hash's placement group
// (Objecter::list_nobjects_seek and list_nobjects, src/osdc/Objecter.cc
// :3761-3773 and :3828-3835 at v19.2.6, :3934-3946 and :4001-4008 at
// v20.2.4). ListPage skips what that replays.
func (p *pool) ListObjectsFrom(ctx context.Context, token string, limit int, fn func(oid, locator string) error) (next string, more bool, err error) {
	h, err := p.state.acquire("list objects", "")
	if err != nil {
		return "", false, err
	}
	defer p.state.release(h)
	iter, err := h.ioctx.Iter()
	if err != nil {
		return "", false, toSeamError("list objects", err)
	}
	defer iter.Close()
	return radosclient.ListPage(ctx, iterSource{iter}, p.state.namespace, token, limit, fn)
}

// iterSource is a librados listing as a radosclient.ListSource.
type iterSource struct{ iter *rados.Iter }

func (s iterSource) Seek(h uint32) { s.iter.Seek(rados.IterToken(h)) }

func (s iterSource) Next() (oid, locator string, ok bool) {
	if !s.iter.Next() {
		return "", "", false
	}
	return s.iter.Value(), s.iter.Locator(), true
}

func (s iterSource) Err() error { return toSeamError("list objects", s.iter.Err()) }
