package op

import (
	"context"
	"io"
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
