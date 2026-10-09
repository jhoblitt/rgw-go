package s3

import (
	"cmp"
	"context"
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// The bucket listings are RGWListBucket_ObjStore_S3 and _S3v2
// (rgw_rest_s3.cc:1710-2116 at v19.2.6, :1821-2227 at v20.2.4). Their
// send_response sends the headers with end_header(...,
// CHUNKED_TRANSFER_ENCODING) once the op has run, then the whole document
// in the one flush it ends with; a failed op is an ordinary error document.
// A bare line number below is v19.2.6's rgw_rest_s3.cc.

// xmlnsS3 is XMLNS_AWS_S3, the namespace every listing's root carries.
const xmlnsS3 = "http://s3.amazonaws.com/doc/2006-03-01/"

// listPrefix is one CommonPrefixes section.
type listPrefix struct {
	Prefix xmltext.Text `xml:"Prefix"`
}

// listContents is one Contents section of a v1 or v2 listing (:1931-1956,
// :2082-2101). Owner is nil where v2 leaves it out.
type listContents struct {
	Key          xmltext.Text `xml:"Key"`
	LastModified xmltext.Text `xml:"LastModified"`
	ETag         xmltext.Text `xml:"ETag"`
	Size         uint64       `xml:"Size"`
	StorageClass xmltext.Text `xml:"StorageClass"`
	Owner        *listOwner   `xml:"Owner"`
	Type         xmltext.Text `xml:"Type"`
}

// listBucketResult is the v1 document (:1916-1966): send_common_response's
// elements, the Contents, then Marker and NextMarker.
type listBucketResult struct {
	XMLName        xml.Name       `xml:"ListBucketResult"`
	Xmlns          string         `xml:"xmlns,attr"`
	EncodingType   xmltext.Text   `xml:"EncodingType,omitempty"`
	Tenant         xmltext.Text   `xml:"Tenant,omitempty"`
	Name           xmltext.Text   `xml:"Name"`
	Prefix         xmltext.Text   `xml:"Prefix"`
	MaxKeys        int            `xml:"MaxKeys"`
	Delimiter      xmltext.Text   `xml:"Delimiter,omitempty"`
	IsTruncated    bool           `xml:"IsTruncated"`
	CommonPrefixes []listPrefix   `xml:"CommonPrefixes"`
	Contents       []listContents `xml:"Contents"`
	Marker         xmltext.Text   `xml:"Marker"`
	NextMarker     xmltext.Text   `xml:"NextMarker,omitempty"`
}

// listBucketV2Result is the v2 document (:2072-2114).
type listBucketV2Result struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	Xmlns                 string         `xml:"xmlns,attr"`
	EncodingType          xmltext.Text   `xml:"EncodingType,omitempty"`
	Tenant                xmltext.Text   `xml:"Tenant,omitempty"`
	Name                  xmltext.Text   `xml:"Name"`
	Prefix                xmltext.Text   `xml:"Prefix"`
	MaxKeys               int            `xml:"MaxKeys"`
	Delimiter             xmltext.Text   `xml:"Delimiter,omitempty"`
	IsTruncated           bool           `xml:"IsTruncated"`
	CommonPrefixes        []listPrefix   `xml:"CommonPrefixes"`
	Contents              []listContents `xml:"Contents"`
	ContinuationToken     *xmltext.Text  `xml:"ContinuationToken"`
	NextContinuationToken xmltext.Text   `xml:"NextContinuationToken,omitempty"`
	KeyCount              int            `xml:"-"`
	StartAfter            *xmltext.Text  `xml:"StartAfter"`
}

// listVersion is one Version or DeleteMarker section of a versions listing
// (:1826-1861, :1993-2025), its name in XMLName. A delete marker has no
// ETag, Size or StorageClass, and v2 writes no Type.
type listVersion struct {
	XMLName      xml.Name
	Key          xmltext.Text `xml:"Key"`
	VersionID    xmltext.Text `xml:"VersionId"`
	IsLatest     bool         `xml:"IsLatest"`
	LastModified xmltext.Text `xml:"LastModified"`
	ETag         xmltext.Text `xml:"ETag,omitempty"`
	Size         *uint64      `xml:"Size"`
	StorageClass xmltext.Text `xml:"StorageClass,omitempty"`
	Owner        *listOwner   `xml:"Owner"`
	Type         xmltext.Text `xml:"Type,omitempty"`
}

// listVersionsResult is the v1 versions document (:1799-1869).
type listVersionsResult struct {
	XMLName             xml.Name      `xml:"ListVersionsResult"`
	Xmlns               string        `xml:"xmlns,attr"`
	EncodingType        xmltext.Text  `xml:"EncodingType,omitempty"`
	Tenant              xmltext.Text  `xml:"Tenant,omitempty"`
	Name                xmltext.Text  `xml:"Name"`
	Prefix              xmltext.Text  `xml:"Prefix"`
	MaxKeys             int           `xml:"MaxKeys"`
	Delimiter           xmltext.Text  `xml:"Delimiter,omitempty"`
	IsTruncated         bool          `xml:"IsTruncated"`
	CommonPrefixes      []listPrefix  `xml:"CommonPrefixes"`
	KeyMarker           xmltext.Text  `xml:"KeyMarker"`
	VersionIDMarker     xmltext.Text  `xml:"VersionIdMarker"`
	NextKeyMarker       *xmltext.Text `xml:"NextKeyMarker"`
	NextVersionIDMarker *xmltext.Text `xml:"NextVersionIdMarker"`
	Versions            []listVersion
}

// listV2TrailingPrefix is a CommonPrefixes section the v2 versions document
// repeats after its versions, each carrying KeyCount and, when sent,
// StartAfter (:2033-2046).
type listV2TrailingPrefix struct {
	XMLName    xml.Name      `xml:"CommonPrefixes"`
	Prefix     xmltext.Text  `xml:"Prefix"`
	KeyCount   int           `xml:"KeyCount"`
	StartAfter *xmltext.Text `xml:"StartAfter"`
}

// listVersionsV2Result is the v2 versions document (:1970-2051), as
// radosgw writes it: the continuation tokens before EncodingType, versions
// without Type, delete markers named DeleteContinuationToken, and the
// common prefixes twice, the second time each with KeyCount and StartAfter
// (docs/ceph-upstream-bugs.md, "radosgw's ListObjectsV2 with versions
// renders a malformed ListVersionsResult").
type listVersionsV2Result struct {
	XMLName                        xml.Name      `xml:"ListVersionsResult"`
	Xmlns                          string        `xml:"xmlns,attr"`
	Tenant                         xmltext.Text  `xml:"Tenant,omitempty"`
	Name                           xmltext.Text  `xml:"Name"`
	Prefix                         xmltext.Text  `xml:"Prefix"`
	MaxKeys                        int           `xml:"MaxKeys"`
	Delimiter                      xmltext.Text  `xml:"Delimiter,omitempty"`
	IsTruncated                    bool          `xml:"IsTruncated"`
	CommonPrefixes                 []listPrefix  `xml:"CommonPrefixes"`
	KeyContinuationToken           xmltext.Text  `xml:"KeyContinuationToken"`
	VersionIDContinuationToken     xmltext.Text  `xml:"VersionIdContinuationToken"`
	NextKeyContinuationToken       *xmltext.Text `xml:"NextKeyContinuationToken"`
	NextVersionIDContinuationToken *xmltext.Text `xml:"NextVersionIdContinuationToken"`
	EncodingType                   xmltext.Text  `xml:"EncodingType,omitempty"`
	// Tail is the versions, then the repeated common prefixes: a
	// listVersion or a listV2TrailingPrefix each, named by its XMLName.
	Tail []any `xml:",any"`
}

// listObjects is RGWListBucket_ObjStore_S3: GET /<bucket>, list-type 1, and
// the versions listing.
func listObjects(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	return serveListBucket(ctx, w, r, false)
}

// listObjectsV2 is RGWListBucket_ObjStore_S3v2: GET /<bucket>?list-type=2.
func listObjectsV2(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	return serveListBucket(ctx, w, r, true)
}

// serveListBucket reads the query as get_common_params and get_params do
// (:1710-1771), runs the op and renders its document.
func serveListBucket(ctx context.Context, w http.ResponseWriter, r *op.Request, v2 bool) error {
	q := r.Query
	o := &op.ListObjects{
		V2:             v2,
		ListVersions:   q.Has("versions"),
		Prefix:         q.Get("prefix"),
		Delimiter:      q.Get("delimiter"),
		MaxKeys:        q.Get("max-keys"),
		EncodingType:   q.Get("encoding-type"),
		AllowUnordered: argBool(q, "allow-unordered"),
	}
	versionIDMarker := ""
	switch {
	case v2:
		o.FetchOwner = argBool(q, "fetch-owner")
		o.StartAfter, o.HasStartAfter = arg(q, "start-after")
		o.ContinuationToken, o.HasToken = arg(q, "continuation-token")
		o.Marker = o.StartAfter
		if o.HasToken {
			o.Marker = o.ContinuationToken
		}
	case o.ListVersions:
		o.Marker = q.Get("key-marker")
		versionIDMarker = q.Get("version-id-marker")
	default:
		o.Marker = q.Get("marker")
	}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	l := listing{
		o: o, r: r, encode: strings.EqualFold(rgwtext.CString(o.EncodingType), "url"),
		tentacle: r.Env.Zone != nil && r.Env.Zone.Release() >= denc.Tentacle,
	}
	var doc any
	switch {
	case v2 && o.ListVersions:
		doc = l.versionsV2()
	case o.ListVersions:
		doc = l.versions(versionIDMarker)
	case v2:
		doc = l.v2()
	default:
		doc = l.v1()
	}
	writeChunkedXML(ctx, w, r, doc)
	return nil
}

// listing renders one listing's document.
type listing struct {
	o *op.ListObjects
	r *op.Request
	// encode is encoding-type=url, case-insensitively: dump_urlsafe's
	// encode_key.
	encode   bool
	tentacle bool
}

// urlsafe is dump_urlsafe (rgw_rest.h:712-721 at v19.2.6 and v20.2.4).
func (l listing) urlsafe(encode bool, s string, encodeSlash bool) xmltext.Text {
	if encode {
		return xmltext.Text(op.URLEncode(s, encodeSlash))
	}
	return xmltext.Text(s)
}

// truncated is "max && is_truncated", the IsTruncated every listing writes.
func (l listing) truncated() bool { return l.o.Max != 0 && l.o.Result.Truncated }

// nextMarker is the next marker when it is written: when truncated and not
// empty.
func (l listing) nextMarker() (string, bool) {
	res := l.o.Result
	return res.NextMarker, res.Truncated && res.NextMarker != ""
}

// prefixes is the CommonPrefixes sections.
func (l listing) prefixes(encode bool) []listPrefix {
	out := make([]listPrefix, 0, len(l.o.Result.CommonPrefixes))
	for _, p := range l.o.Result.CommonPrefixes {
		out = append(out, listPrefix{Prefix: l.urlsafe(encode, p, false)})
	}
	return out
}

// lastModified is dump_time, rgw_to_iso8601 with milliseconds, which
// Tentacle's listings first cut to whole seconds (dump_time_exact_seconds,
// rgw_rest.cc:506-509 at v20.2.4).
func (l listing) lastModified(t time.Time) xmltext.Text {
	if l.tentacle {
		t = t.Truncate(time.Second)
	}
	return xmltext.Text(ISO8601(t))
}

// owner is dump_owner of the entry's stored owner and display name.
func owner(e *op.ObjectEntry) *listOwner {
	return &listOwner{ID: xmltext.Text(e.Owner.String()), DisplayName: xmltext.Text(e.OwnerDisplayName)}
}

// typeOf is the Type element: Appendable for an appended object.
func typeOf(e *op.ObjectEntry) xmltext.Text {
	if e.Appendable {
		return "Appendable"
	}
	return "Normal"
}

// contents is the Contents sections, with the owner when withOwner.
func (l listing) contents(withOwner bool) []listContents {
	out := make([]listContents, 0, len(l.o.Result.Entries))
	for i := range l.o.Result.Entries {
		e := &l.o.Result.Entries[i]
		c := listContents{
			Key:          l.urlsafe(l.encode, e.Key.Name, true),
			LastModified: l.lastModified(e.Mtime),
			ETag:         quotedETag(e.ETag),
			Size:         e.Size,
			StorageClass: xmltext.Text(cmp.Or(e.StorageClass, meta.StorageClassStandard)),
			Type:         typeOf(e),
		}
		if withOwner {
			c.Owner = owner(e)
		}
		out = append(out, c)
	}
	return out
}

// quotedETag is dump_format("ETag", "\"%s\"", etag.c_str()).
func quotedETag(etag string) xmltext.Text {
	return xmltext.Text(`"` + rgwtext.CString(etag) + `"`)
}

// v1 is RGWListBucket_ObjStore_S3::send_response's document (:1916-1966).
func (l listing) v1() listBucketResult {
	o, r := l.o, l.r
	doc := listBucketResult{
		Xmlns: xmlnsS3, Tenant: xmltext.Text(r.Tenant), Name: xmltext.Text(r.Bucket), Prefix: xmltext.Text(o.Prefix),
		MaxKeys: o.Max, IsTruncated: l.truncated(), CommonPrefixes: l.prefixes(l.encode), Contents: l.contents(true),
		Marker: xmltext.Text(o.Marker),
	}
	if l.encode {
		doc.EncodingType = "url"
	}
	if o.Delimiter != "" {
		doc.Delimiter = l.urlsafe(l.encode, o.Delimiter, false)
	}
	if next, ok := l.nextMarker(); ok {
		doc.NextMarker = l.urlsafe(l.encode, next, true)
	}
	return doc
}

// v2 is RGWListBucket_ObjStore_S3v2::send_response's document
// (:2072-2114). The tokens and StartAfter are written as sent, never
// URL-encoded.
func (l listing) v2() listBucketV2Result {
	o, r := l.o, l.r
	doc := listBucketV2Result{
		Xmlns: xmlnsS3, Tenant: xmltext.Text(r.Tenant), Name: xmltext.Text(r.Bucket), Prefix: xmltext.Text(o.Prefix),
		MaxKeys: o.Max, IsTruncated: l.truncated(), CommonPrefixes: l.prefixes(l.encode), Contents: l.contents(o.FetchOwner),
		KeyCount: len(o.Result.Entries) + len(o.Result.CommonPrefixes),
	}
	if l.encode {
		doc.EncodingType = "url"
	}
	if o.Delimiter != "" {
		doc.Delimiter = l.urlsafe(l.encode, o.Delimiter, false)
	}
	if o.HasToken {
		doc.ContinuationToken = new(xmltext.Text(o.ContinuationToken))
	}
	if next, ok := l.nextMarker(); ok {
		doc.NextContinuationToken = xmltext.Text(next)
	}
	if o.HasStartAfter {
		doc.StartAfter = new(xmltext.Text(o.StartAfter))
	}
	return doc
}

// versionEntries is the Version and DeleteMarker sections; v2 names a delete
// marker DeleteContinuationToken, writes the owner only when fetched, and no
// Type. An entry without an instance is the "null" version.
func (l listing) versionEntries(v2 bool) []listVersion {
	out := make([]listVersion, 0, len(l.o.Result.Entries))
	for i := range l.o.Result.Entries {
		e := &l.o.Result.Entries[i]
		v := listVersion{
			XMLName:      xml.Name{Local: "Version"},
			Key:          l.urlsafe(l.encode, e.Key.Name, true),
			VersionID:    xmltext.Text(cmp.Or(e.Key.Instance, "null")),
			IsLatest:     e.IsLatest,
			LastModified: l.lastModified(e.Mtime),
		}
		if e.DeleteMarker {
			v.XMLName.Local = "DeleteMarker"
			if v2 {
				v.XMLName.Local = "DeleteContinuationToken"
			}
		} else {
			v.ETag = quotedETag(e.ETag)
			v.Size = new(e.Size)
			v.StorageClass = xmltext.Text(cmp.Or(e.StorageClass, meta.StorageClassStandard))
		}
		if !v2 || l.o.FetchOwner {
			v.Owner = owner(e)
		}
		if !v2 {
			v.Type = typeOf(e)
		}
		out = append(out, v)
	}
	return out
}

// versions is RGWListBucket_ObjStore_S3::send_versioned_response's document
// (:1799-1869). Its Delimiter, KeyMarker and VersionIdMarker are written as
// sent; the next version marker of a key without an instance is "null".
func (l listing) versions(versionIDMarker string) listVersionsResult {
	o, r := l.o, l.r
	doc := listVersionsResult{
		Xmlns: xmlnsS3, Tenant: xmltext.Text(r.Tenant), Name: xmltext.Text(r.Bucket), Prefix: xmltext.Text(o.Prefix),
		MaxKeys: o.Max, Delimiter: xmltext.Text(o.Delimiter), IsTruncated: l.truncated(), CommonPrefixes: l.prefixes(l.encode),
		KeyMarker: xmltext.Text(o.Marker), VersionIDMarker: xmltext.Text(versionIDMarker), Versions: l.versionEntries(false),
	}
	if l.encode {
		doc.EncodingType = "url"
	}
	if next, ok := l.nextMarker(); ok {
		doc.NextKeyMarker = new(l.urlsafe(l.encode, next, true))
		doc.NextVersionIDMarker = new(xmltext.Text("null"))
	}
	return doc
}

// versionsV2 is RGWListBucket_ObjStore_S3v2::send_versioned_response's
// document (:1970-2051). encode_key is set only after the first common
// prefixes are written, so those are never URL-encoded.
func (l listing) versionsV2() listVersionsV2Result {
	o, r := l.o, l.r
	doc := listVersionsV2Result{
		Xmlns: xmlnsS3, Tenant: xmltext.Text(r.Tenant), Name: xmltext.Text(r.Bucket), Prefix: xmltext.Text(o.Prefix),
		MaxKeys: o.Max, Delimiter: xmltext.Text(o.Delimiter), IsTruncated: l.truncated(), CommonPrefixes: l.prefixes(false),
		KeyContinuationToken: xmltext.Text(o.Marker),
	}
	if next, ok := l.nextMarker(); ok {
		doc.NextKeyContinuationToken = new(xmltext.Text(next))
		doc.NextVersionIDContinuationToken = new(xmltext.Text(""))
	}
	if l.encode {
		doc.EncodingType = "url"
	}
	versions := l.versionEntries(true)
	for i := range versions {
		doc.Tail = append(doc.Tail, versions[i])
	}
	for _, p := range o.Result.CommonPrefixes {
		t := listV2TrailingPrefix{Prefix: l.urlsafe(l.encode, p, false), KeyCount: len(o.Result.Entries)}
		if o.HasStartAfter {
			t.StartAfter = new(xmltext.Text(o.StartAfter))
		}
		doc.Tail = append(doc.Tail, t)
	}
	return doc
}

// arg is RGWHTTPArgs::get(name, &exists): the value and whether it was sent.
func arg(q url.Values, name string) (string, bool) {
	return q.Get(name), q.Has(name)
}

// argBool is RGWHTTPArgs::get_bool with a default of false
// (rgw_common.cc:1001-1037 at v19.2.6): true only for "true", in any case;
// "false" and any other value are false.
func argBool(q url.Values, name string) bool {
	return strings.EqualFold(rgwtext.CString(q.Get(name)), "true")
}
