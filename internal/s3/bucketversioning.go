package s3

import (
	"bytes"
	"context"
	"fmt"
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

// versioningSchema is VersioningConfiguration holding Status and MfaDelete,
// each once, in S3's namespace. It is read leniently: an element it does not
// name directly under the root, text between the elements and an attribute
// other than a namespace declaration are read as radosgw reads them
// (docs/differences.md, "PutBucketVersioning answers MalformedXML for a
// document that does not parse").
var versioningSchema = &xmltext.Schema{
	Root:       "VersioningConfiguration",
	Children:   map[string][]xmltext.Child{"VersioningConfiguration": {{Name: "Status"}, {Name: "MfaDelete"}}},
	Namespaces: []string{xmlnsS3},
	Lenient:    true,
}

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
// AWS's 400 MalformedXML (docs/differences.md, "PutBucketVersioning answers
// MalformedXML for a document that does not parse").
//
// Before it builds any tree, xmltext.Scan refuses, against versioningSchema,
// what radosgw reads but rgw-go does not; xmltext.Parse then refuses what
// expat refuses. Client-chosen text in a refusal is cut by bytes before it is
// quoted, so a refusal's text stays small however its runes quote.
func parseVersioningConfiguration(body []byte) (op.VersioningStatus, *bool, error) {
	if err := xmltext.Scan(body, versioningSchema, nil); err != nil {
		return 0, nil, fmt.Errorf("%w: %w", op.ErrMalformedXML, err)
	}
	root, err := xmltext.Parse(body)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %s", op.ErrMalformedXML, xmltext.Clip(err.Error(), 128))
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
		return 0, nil, fmt.Errorf("%w: Status %s", op.ErrMalformedXML, xmltext.Quote(s.Text))
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
	return 0, nil, fmt.Errorf("%w: MfaDelete %s", op.ErrMalformedXML, xmltext.Quote(m.Text))
}
