package driver

import (
	"context"
	"fmt"
	"io"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// ObjectLayout implements op.LayoutStore. Its document is the body
// RGWGetObjLayout sends, RadosObject::dump_obj_layout inside the "result"
// section a JSONFormatter writes without its name
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2780-L2826
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4368-L4385 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3615-L3661
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4879-L4896 at v20.2.4):
// the head as the bucket's placement rule places it, the manifest as the
// store's release dumps it, and every stripe from obj_begin to obj_end, its
// size cut at the end of the object. A cloud tier's S3 secret in the
// manifest is masked, where radosgw writes it to any holder of admin=read
// (docs/exclusions.md, "?layout masks a cloud tier's S3 secret").
//
// ObjectLayout renders the document once to measure it, which is where it
// fails, and WriteTo renders it again as it writes, so a request holds one
// stripe's entry at a time where radosgw buffers the whole document. Both
// passes stop with ctx's error once ctx ends, checked every
// layoutCheckStripes stripes. An
// object without a manifest is InternalError, where radosgw dereferences the
// null manifest (docs/ceph-upstream-bugs.md, "radosgw's ?layout dereferences
// a null manifest"). The walk fails with denc.ErrMalformed at a step that
// does not move forward and with meta.ErrTooManyStripes past meta.MaxStripes
// stripes, both as UnknownError, where radosgw's loop never ends or renders
// any size (docs/exclusions.md, "A manifest walk that stops moving, or runs
// past its stripe bound, fails").
func (s *Store) ObjectLayout(ctx context.Context, st *op.ObjectState) (op.LayoutDocument, error) {
	d, err := s.layout(ctx, st, meta.MaxStripes)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// layoutCheckStripes is how many stripes a pass writes between two checks of
// its context.
const layoutCheckStripes = 4096

// layout is ObjectLayout with limit standing for meta.MaxStripes.
func (s *Store) layout(ctx context.Context, st *op.ObjectState, limit uint64) (*layoutDoc, error) {
	if !st.Exists {
		return nil, fmt.Errorf("laying out %s: %w", objName(st), op.ErrNoSuchKey)
	}
	if st.Manifest == nil {
		return nil, fmt.Errorf("%w: %s has no manifest to lay out", op.ErrInternalError, objName(st))
	}
	d := &layoutDoc{ctx: ctx, s: s, st: st, limit: limit}
	var c countingWriter
	if err := d.render(&c); err != nil {
		return nil, err
	}
	d.n = c.n
	return d, nil
}

// layoutDoc is an object's layout document, n bytes long. It keeps the
// request's context because WriteTo, io.WriterTo's, takes none.
type layoutDoc struct {
	ctx   context.Context
	s     *Store
	st    *op.ObjectState
	limit uint64
	n     int64
}

// Len implements op.LayoutDocument.
func (d *layoutDoc) Len() int64 { return d.n }

// WriteTo implements op.LayoutDocument.
func (d *layoutDoc) WriteTo(w io.Writer) (int64, error) {
	c := countingWriter{w: w}
	err := d.render(&c)
	return c.n, err
}

// render writes the document to w. An empty object needs no bound check, and
// end = obj_size - 1 would wrap there.
func (d *layoutDoc) render(w io.Writer) error {
	s, st, m := d.s, d.st, d.st.Manifest
	if m.ObjSize > 0 {
		if err := checkStripeBound(m, 0, m.ObjSize-1, d.limit); err != nil {
			return manifestErr(st, err)
		}
	}
	rec := st.Bucket
	f := formatter.NewJSON(false)
	f.OpenObjectSection("result")
	// st came from StatObject, whose head ref already resolved this pool.
	s.dumpRawObj(f, "head", s.rawObj(rec.Info.PlacementRule, meta.Obj{Bucket: rec.Info.Bucket, Key: st.Key}))
	f.OpenObjectSection("manifest")
	meta.MaskTierSecrets(*m).Dump(f, s.release)
	f.CloseSection()
	if err := f.Err(); err != nil {
		return manifestErr(st, err)
	}
	f.OpenArraySection("data_location")
	opened := len(f.Bytes())
	if _, err := w.Write(f.Bytes()); err != nil {
		return err
	}
	if err := s.writeDataLocation(d.ctx, w, st, d.limit); err != nil {
		return err
	}
	f.CloseSection()
	f.CloseSection()
	_, err := w.Write(f.Bytes()[opened:])
	return err
}

// writeDataLocation writes the entries of dump_obj_layout's data_location to
// w, each on its own: a stripe's offset in the object, its RADOS object as
// obj_to_raw places it, where in that object it starts, and get_stripe_size
// cut at what is left of the object. The comma between two entries is the
// one print_comma writes.
func (s *Store) writeDataLocation(ctx context.Context, w io.Writer, st *op.ObjectState, limit uint64) error {
	m := st.Manifest
	it, err := m.Seek(0)
	if err != nil {
		return manifestErr(st, err)
	}
	e := formatter.NewJSON(false)
	for stripes := uint64(1); !it.Done(); stripes++ {
		if stripes > limit {
			return manifestErr(st, fmt.Errorf("%w: more than %d before offset %d of %d", meta.ErrTooManyStripes, limit, it.Ofs(), m.ObjSize))
		}
		if stripes%layoutCheckStripes == 0 {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("laying out %s: %w", objName(st), err)
			}
		}
		ofs := it.Ofs()
		obj, rule, _ := it.Location()
		e.Reset()
		e.OpenObjectSection("obj")
		e.DumpUnsigned("ofs", ofs)
		s.dumpRawObj(e, "loc", s.rawObj(rule, obj))
		e.DumpUnsigned("loc_ofs", it.LocOfs())
		e.DumpUnsigned("loc_size", min(it.StripeSize(), m.ObjSize-ofs))
		e.CloseSection()
		if stripes > 1 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		if _, err := w.Write(e.Bytes()); err != nil {
			return err
		}
		if err := it.Next(); err != nil {
			return manifestErr(st, err)
		}
		if !it.Done() && it.Ofs() <= ofs {
			return manifestErr(st, fmt.Errorf("%w: manifest iteration does not advance past offset %d of %d", denc.ErrMalformed, ofs, m.ObjSize))
		}
	}
	return nil
}

// dumpRawObj is encode_json(name, rgw_raw_obj).
func (s *Store) dumpRawObj(f formatter.Formatter, name string, r rawObj) {
	f.OpenObjectSection(name)
	meta.RawObj{Pool: r.pool, OID: r.oid, Loc: r.loc}.Dump(f, s.release)
	f.CloseSection()
}

// countingWriter counts what passes through it to w, or discards it when w
// is nil.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n := len(p)
	var err error
	if c.w != nil {
		n, err = c.w.Write(p)
	}
	c.n += int64(n)
	return n, err
}
