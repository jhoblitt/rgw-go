package s3

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// getObjectAttrs serves get_obj_attrs ([T] RGWGetObjAttrs_ObjStore_S3,
// rgw_rest_s3.cc:3941-4137), on both releases. It reads get_params' headers,
// whose refusals the op answers where radosgw's execute does, and the SSE
// inputs rgw_s3_prepare_decrypt reads; it reads none of GetObject's others.
func getObjectAttrs(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	_, sse := op.HeaderValue(r.Header, "x-amz-server-side-encryption")
	o := &op.GetObjectAttributes{
		Versioned:     r.Object.Instance != "",
		SSEHeader:     sse,
		SSECAlgorithm: header(r, "x-amz-server-side-encryption-customer-algorithm"),
		SSECKey:       header(r, "x-amz-server-side-encryption-customer-key"),
		SSECKeyMD5:    header(r, "x-amz-server-side-encryption-customer-key-MD5"),
		Secure:        secure(r),
	}
	paramErr := objectAttrsInput(r, o)
	if err := op.Run(ctx, deferredParams{Op: o, err: paramErr}, r); err != nil {
		return err
	}
	SetCommonHeaders(w, r)
	h := w.Header()
	h.Set("Last-Modified", httpDate(o.State.Mtime))
	if o.VersionID != "" {
		h.Set("x-amz-version-id", o.VersionID)
	}
	h.Set("Content-Type", "application/xml")
	doc := objectAttrsDocument(o)
	h.Set("Content-Length", strconv.Itoa(len(xmlHeader)+len(doc)))
	w.WriteHeader(http.StatusOK)
	// A failed write means the client is gone, with no one left to tell.
	_, _ = w.Write([]byte(xmlHeader + doc)) //nolint:errcheck // see above
	return nil
}

// objectAttrsInput is RGWGetObjAttrs_ObjStore_S3::get_params ([T]
// :3941-3983): x-amz-max-parts, capped at 1000, then
// x-amz-part-number-marker, each through strict_strtol, the first refusal
// ending the parse; then x-amz-object-attributes. x-amz-expected-bucket-owner
// is read and never used, as radosgw reads it.
func objectAttrsInput(r *op.Request, o *op.GetObjectAttributes) error {
	if v, ok := op.HeaderValue(r.Header, "x-amz-max-parts"); ok {
		n, err := parseInt(v)
		if err != nil {
			return op.ErrInvalidPart.WithMessage("Invalid value for MaxParts: " + err.Error())
		}
		o.MaxParts = new(min(n, op.MaxObjectParts))
	}
	if v, ok := op.HeaderValue(r.Header, "x-amz-part-number-marker"); ok {
		n, err := parseInt(v)
		if err != nil {
			return op.ErrInvalidPart.WithMessage("Invalid value for PartNumberMarker: " + err.Error())
		}
		o.PartMarker = &n
	}
	if v, ok := op.HeaderValue(r.Header, "x-amz-object-attributes"); ok {
		o.Requested = op.ParseObjectAttrs(v)
	}
	return nil
}

// objectAttrsDocument is send_response's GetObjectAttributes section ([T]
// :4017-4133), with no namespace: the requested attributes in its order. The
// ETag is the stored attr, unquoted; the Checksum is empty, rgw-go storing
// none (docs/exclusions.md); ObjectParts appears for a multipart object only.
// Every string goes through the formatter's escaping and every number is
// written bare.
func objectAttrsDocument(o *op.GetObjectAttributes) string {
	var b strings.Builder
	b.WriteString("<GetObjectAttributes>")
	attrs := o.State.Attrs
	if o.Requested&op.AttrETag != 0 {
		element(&b, "ETag", xmltext.Escape(string(attrs[meta.AttrETag])))
	}
	if o.Requested&op.AttrChecksum != 0 {
		b.WriteString("<Checksum></Checksum>")
	}
	if o.Requested&op.AttrObjectParts != 0 && o.PartsCount != nil {
		b.WriteString("<ObjectParts>")
		for _, p := range o.Parts {
			b.WriteString("<Part>")
			element(&b, "PartNumber", strconv.Itoa(p.Number))
			element(&b, "Size", strconv.FormatUint(p.Size, 10))
			b.WriteString("</Part>")
		}
		element(&b, "PartsCount", strconv.Itoa(*o.PartsCount))
		element(&b, "TotalPartsCount", strconv.Itoa(*o.PartsCount))
		element(&b, "IsTruncated", strconv.FormatBool(o.PartsTruncated))
		if o.MaxParts != nil {
			element(&b, "MaxParts", strconv.Itoa(*o.MaxParts))
		}
		if o.PartsTruncated {
			element(&b, "NextPartNumberMarker", strconv.Itoa(o.NextPartMarker))
		}
		if o.PartMarker != nil {
			element(&b, "PartNumberMarker", strconv.Itoa(*o.PartMarker))
		}
		b.WriteString("</ObjectParts>")
	}
	if o.Requested&op.AttrObjectSize != 0 {
		element(&b, "ObjectSize", strconv.FormatUint(o.ObjSize, 10))
	}
	if o.Requested&op.AttrStorageClass != 0 {
		class, ok := attrs[meta.AttrStorageClass]
		if !ok {
			class = []byte(meta.StorageClassStandard)
		}
		element(&b, "StorageClass", xmltext.Escape(string(class)))
	}
	b.WriteString("</GetObjectAttributes>")
	return b.String()
}

// element writes <name>text</name>; text is already escaped.
func element(b *strings.Builder, name, text string) {
	b.WriteString("<" + name + ">" + text + "</" + name + ">")
}
