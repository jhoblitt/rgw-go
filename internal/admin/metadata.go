package admin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// maxMetadataBody bounds a metadata put's document. radosgw reads the
// whole body, however long (RGWOp_Metadata_Put::get_data,
// rgw_rest_metadata.cc:176-219).
const maxMetadataBody = 16 << 20

// newMetadataHandlers are RGWHandler_Metadata's routes.
func newMetadataHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"get_metadata":        getMetadata,
		"get_metadata_myself": getMetadataMyself,
		"list_metadata":       listMetadata,
		"set_metadata":        setMetadata,
		"remove_metadata":     removeMetadata,
	}
}

// frameMetadataKey is frame_metadata_key (rgw_rest_metadata.cc:35-52): the
// path's section, then ":" and key when key is not empty; with no section
// in the path, key alone is the whole metadata key.
func frameMetadataKey(q Request, key string) string {
	if q.Sub == "" {
		return key
	}
	if key == "" {
		return q.Sub
	}
	return q.Sub + ":" + key
}

// splitMetadataKey is RGWMetadataManager::parse_metadata_key
// (rgw_metadata.cc:437-446 at v19.2.6, :232-241 at v20.2.4): the section
// before the first ':', the entry after it.
func splitMetadataKey(framed string) (section, entry string) {
	section, entry, _ = strings.Cut(framed, ":")
	return section, entry
}

// getMetadata is RGWOp_Metadata_Get: the key argument as RGWHTTPArgs::get
// holds it.
func getMetadata(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	key, _ := q.Args.Get("key")
	return serveMetadataGet(ctx, w, r, q, frameMetadataKey(q, key))
}

// getMetadataMyself is RGWOp_Metadata_Get_Myself (rgw_rest_metadata.cc:71-75),
// whose key is the requester's owner id.
func getMetadataMyself(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	return serveMetadataGet(ctx, w, r, q, frameMetadataKey(q, r.Identity.Owner.String()))
}

func serveMetadataGet(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request, framed string) error {
	o := &op.GetMetadata{}
	o.Section, o.Key = splitMetadataKey(framed)
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	rel := r.Env.Zone.Release()
	return WriteDumped(w, r, q, func(f formatter.Formatter) { dumpMetadataInfo(f, framed, o.Result, rel) })
}

// dumpMetadataInfo is RGWMetadataManager::get's document
// (rgw_metadata.cc:485-494 at v19.2.6, :280-289 at v20.2.4), which the op
// writes without starting the flusher.
func dumpMetadataInfo(f formatter.Formatter, key string, e op.MetadataEntry, rel denc.Release) {
	f.OpenObjectSection("metadata_info")
	f.DumpString("key", key)
	f.OpenObjectSection("ver")
	e.Version.Dump(f, rel)
	f.CloseSection()
	// real_clock::is_zero: the epoch, which Go's zero time also stands for.
	if !e.Mtime.IsZero() && !e.Mtime.Equal(time.Unix(0, 0)) {
		f.DumpStream("mtime", meta.Time{Time: e.Mtime}.Gmtime())
	}
	f.OpenObjectSection("data")
	if e.Doc != nil {
		e.Doc.Dump(f, rel)
	}
	f.CloseSection()
	f.CloseSection()
}

// listMetadata is RGWOp_Metadata_List's argument handling
// (rgw_rest_metadata.cc:77-108): the marker as RGWHTTPArgs::get holds it,
// and max-entries through strict_strtol cast to unsigned, a value it
// refuses being -EINVAL.
func listMetadata(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := &op.ListMetadata{}
	key, _ := q.Args.Get("key")
	o.Section, _ = splitMetadataKey(frameMetadataKey(q, key))
	o.Marker, _ = q.Args.Get("marker")
	var argErr error
	if s, ok := q.Args.Get("max-entries"); ok {
		o.Extended = true
		n, valid := strictStrtol(s)
		if !valid {
			argErr = fmt.Errorf("%w: max-entries %q", op.ErrInvalidArgument, s)
		}
		o.MaxEntries = uint64(uint32(n)) //nolint:gosec // radosgw casts the int to unsigned
	}
	if err := runChecked(ctx, o, r, argErr); err != nil {
		return err
	}
	return WriteDumped(w, r, q, func(f formatter.Formatter) { dumpMetadataKeys(f, o) })
}

// strictStrtol is ceph's strict_strtol in base 10 over a C string
// (common/strtol.cc:61-73 at v19.2.6 and v20.2.4): strict_strtoll, refused
// outside int's range.
func strictStrtol(s string) (int32, bool) {
	v, ok := rgwtext.StrictStrtoll(rgwtext.CString(s))
	if !ok || v < math.MinInt32 || v > math.MaxInt32 {
		return 0, false
	}
	return int32(v), true
}

// dumpMetadataKeys is RGWOp_Metadata_List's document
// (rgw_rest_metadata.cc:134-170): the bare "keys" array unless max-entries
// was given, which radosgw keeps for backward compatibility (:98), and the
// marker base64-wrapped (:164-168).
func dumpMetadataKeys(f formatter.Formatter, o *op.ListMetadata) {
	if o.Extended {
		f.OpenObjectSection("result")
	}
	f.OpenArraySection("keys")
	for _, k := range o.Keys {
		f.DumpString("key", k)
	}
	f.CloseSection()
	if o.Extended {
		f.DumpBool("truncated", o.Truncated)
		f.DumpUnsigned("count", o.Count)
		if o.Truncated {
			f.DumpString("marker", base64.StdEncoding.EncodeToString([]byte(o.NextMarker)))
		}
		f.CloseSection()
	}
}

// metadataPut is RGWOp_Metadata_Put::execute's order past the cap check
// (rgw_rest_metadata.cc:234-274): the body, then update-type, then the put.
// ran records that execute began, after which send_response sends
// RGWX_UPDATE_VERSION whatever the outcome (:276-288); a refusal before it,
// abort_early's, carries neither header.
type metadataPut struct {
	*op.PutMetadata
	updateType string
	hasType    bool
	ran        bool
}

func (o *metadataPut) Execute(ctx context.Context, r *op.Request) error {
	o.ran = true
	body, err := readMetadataBody(r)
	if err != nil {
		return err
	}
	o.Body = body
	if o.hasType {
		sync, ok := op.ParseSyncType(o.updateType)
		if !ok {
			return fmt.Errorf("%w: update-type %q", op.ErrInvalidArgument, o.updateType)
		}
		o.Sync = sync
	}
	return o.PutMetadata.Execute(ctx, r)
}

// readMetadataBody is RGWOp_Metadata_Put::get_data: a positive
// Content-Length, or a chunked body, else MissingContentLength. The body is
// read to its end, so the authenticator's verdict on a signed payload,
// which comes with the final read, fails the put. A body over
// maxMetadataBody is InvalidArgument, and one that ends short of its
// length RequestTimeout.
func readMetadataBody(r *op.Request) ([]byte, error) {
	switch {
	case r.ContentLength == 0:
		return nil, op.ErrMissingContentLength
	case r.ContentLength > maxMetadataBody:
		return nil, fmt.Errorf("%w: a metadata document over %d bytes", op.ErrInvalidArgument, maxMetadataBody)
	}
	src := r.Body
	if src == nil {
		src = http.NoBody
	}
	b, err := io.ReadAll(io.LimitReader(src, maxMetadataBody+1))
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF):
		return nil, fmt.Errorf("%w: the body ended short of its length", op.ErrRequestTimeout)
	case err != nil:
		return nil, err
	case len(b) > maxMetadataBody:
		return nil, fmt.Errorf("%w: a metadata document over %d bytes", op.ErrInvalidArgument, maxMetadataBody)
	}
	return b, nil
}

// setMetadata is RGWOp_Metadata_Put with its send_response
// (rgw_rest_metadata.cc:234-288). Both headers keep radosgw's spelling,
// which http.Header.Set would canonicalize.
func setMetadata(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	key, _ := q.Args.Get("key")
	o := &metadataPut{PutMetadata: &op.PutMetadata{}}
	o.Section, o.Key = splitMetadataKey(frameMetadataKey(q, key))
	o.updateType, o.hasType = q.Args.Get("update-type")
	err := op.Run(ctx, o, r)
	if o.ran {
		w.Header()["RGWX_UPDATE_VERSION"] = []string{"ver:" + strconv.FormatUint(o.OnDisk.Ver, 10) + ",tag:" + o.OnDisk.Tag}
	}
	if err != nil {
		return err
	}
	status := o.UpdateStatus()
	if status == "" {
		WriteEmpty(w, r, http.StatusOK)
		return nil
	}
	w.Header()["RGWX_UPDATE_STATUS"] = []string{status}
	WriteEmpty(w, r, http.StatusNoContent)
	return nil
}

// removeMetadata is RGWOp_Metadata_Delete (rgw_rest_metadata.cc:290-300),
// which sends no body.
func removeMetadata(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	key, _ := q.Args.Get("key")
	o := &op.RemoveMetadata{}
	o.Section, o.Key = splitMetadataKey(frameMetadataKey(q, key))
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}
