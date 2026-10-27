package op

import (
	"context"
	"io"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

//counterfeiter:generate . LayoutStore

// LayoutStore reads where an object's data sits in RADOS. It stays off
// ObjectStore, which every data path calls.
type LayoutStore interface {
	// ObjectLayout is st's head object, manifest and data locations, the
	// document RadosObject::dump_obj_layout writes
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2780-L2826 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3615-L3661 at v20.2.4).
	// It fails before any of the document is written, so that its error can
	// still be the response, and the document is nil with every error.
	ObjectLayout(ctx context.Context, st *ObjectState) (LayoutDocument, error)
}

//counterfeiter:generate . LayoutDocument

// LayoutDocument is a layout ready to send. WriteTo writes exactly Len
// bytes, rendering them as it writes, so a response can state its
// Content-Length without the document being held whole: one object's
// document runs to hundreds of MB. Len holds only while the *ObjectState
// given to ObjectLayout stays unchanged until WriteTo returns, as both
// render from it. WriteTo checks the context given to ObjectLayout every
// 4096 stripes and stops with its error once it has ended, so a document of
// fewer stripes runs to completion.
type LayoutDocument interface {
	io.WriterTo
	Len() int64
}

// GetObjectLayout is RGWGetObjLayout, ?layout
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L2366-L2383 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L7904-L7911 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L2537-L2554 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L8809-L8816 at v20.2.4):
// the object's layout for a holder of the admin=read cap.
type GetObjectLayout struct {
	// Document is the layout Execute laid out, which the protocol handler
	// sends.
	Document LayoutDocument
}

var _ Op = (*GetObjectLayout)(nil)

// Name is RGWGetObjLayout::name.
func (o *GetObjectLayout) Name() string { return "get_obj_layout" }

// Action is ActionNone: RGWGetObjLayout's verify_permission evaluates no
// policy.
func (o *GetObjectLayout) Action() policy.Action { return policy.ActionNone }

// OpMask is RGW_OP_TYPE_READ.
func (o *GetObjectLayout) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket and the object's head: RGWGetObjLayout prefetches
// no data.
func (o *GetObjectLayout) Init(ctx context.Context, r *Request) error {
	return statHead(ctx, r)
}

// VerifyPermission is RGWGetObjLayout::verify_permission, check_caps on the
// requester's caps for admin=read, which consults no ACL or policy, so the
// bucket's owner without the cap is refused. Before it come the refusals
// init_permissions and read_permissions make for every object op, a missing
// object's among them (VerifyObjectPrelude).
func (o *GetObjectLayout) VerifyPermission(ctx context.Context, r *Request) error {
	if err := VerifyObjectPrelude(ctx, r); err != nil {
		return err
	}
	return CheckCaps(r, "admin", meta.CapRead)
}

// Execute lays out the object Init read, before anything is sent, so that
// a layout that fails is the request's error. radosgw's execute does
// nothing, and its send_response sends 200 before dump_obj_layout runs and
// ignores its error (docs/exclusions.md, "?layout answers a layout that
// fails with its error").
func (o *GetObjectLayout) Execute(ctx context.Context, r *Request) error {
	doc, err := r.Env.Layout.ObjectLayout(ctx, r.ObjState)
	if err != nil {
		return err
	}
	o.Document = doc
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *GetObjectLayout) Complete(context.Context, *Request) {}
