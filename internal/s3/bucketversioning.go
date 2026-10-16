package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// The bucket versioning routes are rgw_rest_s3.cc's
// RGWGetBucketVersioning_ObjStore_S3 and RGWSetBucketVersioning_ObjStore_S3.

// getBucketVersioning is get_bucket_versioning
// (RGWGetBucketVersioning_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2160-L2177 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2255-L2272 at v20.2.4):
// VersioningConfiguration in S3's namespace, holding Status and MfaDelete
// once the bucket was ever versioned and nothing before.
func getBucketVersioning(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetBucketVersioning{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	var b bytes.Buffer
	b.WriteString(`<VersioningConfiguration xmlns="` + xmlnsS3 + `">`)
	if o.Versioned {
		b.WriteString("<Status>" + enabledOr(o.Enabled, "Suspended") + "</Status>")
		b.WriteString("<MfaDelete>" + enabledOr(o.MFAEnabled, "Disabled") + "</MfaDelete>")
	}
	b.WriteString("</VersioningConfiguration>")
	writeDocument(w, r, b.Bytes())
	return nil
}

// enabledOr is "Enabled" when on, and otherwise off.
func enabledOr(on bool, off string) string {
	if on {
		return "Enabled"
	}
	return off
}

// putBucketVersioning is set_bucket_versioning:
// RGWSetBucketVersioning_ObjStore_S3::get_params
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2212-L2269 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2307-L2364 at v20.2.4),
// the body read up to rgw_max_put_param_size and parsed by
// parseVersioningConfiguration; 200 with no body (writeEmptyXML, as
// send_response ends the header and writes nothing,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2271-L2277 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2366-L2372 at v20.2.4).
func putBucketVersioning(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.PutBucketVersioning{Params: func(_ context.Context, o *op.PutBucketVersioning) error {
		body, err := readParamBody(r, invalidRange)
		if err != nil {
			return err
		}
		o.Status, o.MFADelete, err = parseVersioningConfiguration(body)
		return err
	}}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeEmptyXML(w, r, http.StatusOK)
	return nil
}

// maxVersioningDepth is the deepest a VersioningConfiguration's elements
// nest: the root, then Status and MfaDelete, which hold only text.
const maxVersioningDepth = 2

// parseVersioningConfiguration reads body as get_params does, with
// ver_config_status
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2179-L2210 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2274-L2305 at v20.2.4):
// the root VersioningConfiguration holding a Status of exactly Enabled or
// Suspended, and an optional MfaDelete of exactly Enabled or Disabled, nil
// when absent. Each is the element's own text, as decode_xml_obj takes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L179-L182 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.h#L179-L182 at v20.2.4),
// untrimmed and compared case for case. radosgw answers every refusal here
// -EINVAL, 400 InvalidArgument, a missing Status included; rgw-go answers
// AWS's 400 MalformedXML (docs/exclusions.md, "PutBucketVersioning answers
// MalformedXML for a document that does not parse").
//
// Before it builds any tree, scanVersioningConfiguration refuses what
// radosgw reads but rgw-go does not; xmltext.Parse then refuses what expat
// refuses.
func parseVersioningConfiguration(body []byte) (op.VersioningStatus, *bool, error) {
	if err := scanVersioningConfiguration(body); err != nil {
		return 0, nil, err
	}
	root, err := xmltext.Parse(body)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", op.ErrMalformedXML, err)
	}
	if root.Name != "VersioningConfiguration" {
		return 0, nil, fmt.Errorf("%w: the root is %q, not VersioningConfiguration", op.ErrMalformedXML, root.Name)
	}
	var status op.VersioningStatus
	switch s := root.FindFirst("Status"); {
	case s == nil:
		return 0, nil, fmt.Errorf("%w: no Status", op.ErrMalformedXML)
	case s.Text == "Enabled":
		status = op.VersioningEnabled
	case s.Text == "Suspended":
		status = op.VersioningSuspended
	default:
		return 0, nil, fmt.Errorf("%w: Status %q", op.ErrMalformedXML, s.Text)
	}
	m := root.FindFirst("MfaDelete")
	switch {
	case m == nil:
		return status, nil, nil
	case m.Text == "Enabled":
		return status, new(true), nil
	case m.Text == "Disabled":
		return status, new(false), nil
	}
	return 0, nil, fmt.Errorf("%w: MfaDelete %q", op.ErrMalformedXML, m.Text)
}

// scanVersioningConfiguration reads body's tokens and stops at the first of
// these, which radosgw's expat reads: a document type declaration, whose
// internal subset can declare entities expat expands, as RGWXMLParser sets
// no handler that would stop it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L213-L223 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L213-L223 at v20.2.4);
// an element nested deeper than maxVersioningDepth; a second
// Status or MfaDelete under the root, where RGWXMLDecoder::decode_xml reads
// the first
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L219-L241 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.h#L219-L241 at v20.2.4);
// an element name with a prefix, which radosgw, with namespace processing off
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L158 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L158 at v20.2.4),
// reads as another name; and a namespace declaration naming anything but
// S3's namespace. Each is MalformedXML. It keeps no tree and stops at the
// refusal, so a refused body costs a read of the tokens before it.
func scanVersioningConfiguration(body []byte) error {
	d := xml.NewDecoder(bytes.NewReader(body))
	depth := 0
	var status, mfaDelete bool
	for {
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %w", op.ErrMalformedXML, err)
		}
		switch t := tok.(type) {
		case xml.Directive:
			return fmt.Errorf("%w: a document type or other declaration", op.ErrMalformedXML)
		case xml.EndElement:
			if depth == 0 {
				return fmt.Errorf("%w: an end tag without a start tag", op.ErrMalformedXML)
			}
			depth--
		case xml.StartElement:
			depth++
			if depth > maxVersioningDepth {
				return fmt.Errorf("%w: an element nested deeper than %d", op.ErrMalformedXML, maxVersioningDepth)
			}
			if t.Name.Space != "" {
				return fmt.Errorf("%w: an element name with the prefix %q", op.ErrMalformedXML, t.Name.Space)
			}
			for _, a := range t.Attr {
				if declaresNamespace(a) && a.Value != xmlnsS3 {
					return fmt.Errorf("%w: the namespace %q, not S3's", op.ErrMalformedXML, a.Value)
				}
			}
			if depth != 2 {
				continue
			}
			seen := &status
			switch t.Name.Local {
			case "Status":
			case "MfaDelete":
				seen = &mfaDelete
			default:
				continue
			}
			if *seen {
				return fmt.Errorf("%w: a repeated %s", op.ErrMalformedXML, t.Name.Local)
			}
			*seen = true
		}
	}
}

// declaresNamespace reports whether a, as RawToken reads it, is xmlns or
// xmlns:prefix.
func declaresNamespace(a xml.Attr) bool {
	return a.Name.Space == "xmlns" || a.Name.Space == "" && a.Name.Local == "xmlns"
}
