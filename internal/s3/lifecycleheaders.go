package s3

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// setReadExpiration adds x-amz-expiration to a GET's or HEAD's success
// headers, as send_response_data computes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L397 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L470 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L400 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L498 at v20.2.4)
// for o's read. The state served, o.State, gives the mtime, the Last-Modified
// the read sends, and the tags attr RGWGetObj::execute decodes into s->tagset
// before every read rgw-go serves (storedTags). The size is the object's own,
// o.CondState's, which a partNumber read keeps apart from its part's, as the
// worker's size filter reads it (lifecycleSize).
func setReadExpiration(h http.Header, r *op.Request, o *op.GetObject) {
	if c, ok := storedLifecycle(r); ok {
		addExpiration(h, r, c, storedTags(o.State.Attrs[tags.Attr]), o.State.Mtime, lifecycleSize(o.CondState))
	}
}

// setWriteExpiration adds x-amz-expiration to a PutObject's success headers,
// as send_response computes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2750 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2903 at v20.2.4),
// for the object written at mtime. Its tags are requested, the request's
// x-amz-tagging, nil without one: encode_obj_tags_attr stores a set that is
// not empty and rgw_cond_decode_objtags reads it back into s->tagset
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4531-L4532 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4799-L4800 at v20.2.4).
// Without request tags radosgw matches the tags verify_permission left in
// s->tagset, the bucket's under a policy naming s3:ResourceTag; rgw-go
// matches none (docs/differences.md, "A PutObject's x-amz-expiration matches
// the object's own tags"). The size is the bytes the op read, r.BytesIn,
// known for a body of unknown length too: the plaintext the store wrote,
// which the worker's size filter reads back as the object's size or, for an
// object the store compressed, as its compression info's orig_size.
func setWriteExpiration(h http.Header, r *op.Request, requested *tags.Set, mtime time.Time) {
	c, ok := storedLifecycle(r)
	if !ok {
		return
	}
	var objTags tags.Set
	if requested != nil {
		objTags = *requested
	}
	addExpiration(h, r, c, objTags, mtime, uint64(max(r.BytesIn, 0)))
}

// addExpiration adds the x-amz-expiration lifecycle.ExpirationHeader gives c
// for r's key, and none when it gives none, as dump_header_if_nonempty sends
// it. The key is get_s3_expiration_header's at v19.2.7, the object's name
// with the versionId the request names
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L112-L126 at v19.2.7):
// the query's, which a website read keeps when its retarget gives r.Object
// another key and no instance.
func addExpiration(h http.Header, r *op.Request, c lifecycle.Configuration, objTags tags.Set, mtime time.Time, size uint64) {
	if v := lifecycle.ExpirationHeader(c, r.Object.Name, r.Query.Get("versionId"), objTags, mtime, size); v != "" {
		addHeader(h, "x-amz-expiration", v)
	}
}

// lifecycleSize is st's size as the lifecycle worker's size filter reads it,
// lcProbe.Size's (internal/driver/lc_actions.go:540-551): a compressed
// object's uncompressed size, and any other object's size.
func lifecycleSize(st *op.ObjectState) uint64 {
	if st.Compression != nil {
		return st.Compression.OrigSize
	}
	return st.Size
}

// setAbortHeaders adds RGWInitMultipart_ObjStore_S3::send_response's
// x-amz-abort-date, in dump_time_header's format, and x-amz-abort-rule-id
// when the rule's ID is not empty
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4066-L4072 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4533-L4539 at v20.2.4),
// as lifecycle.AbortHeader gives them for an upload of r's key initiated at
// initiated. radosgw passes an mtime RGWInitMultipart never sets, so it
// counts from the epoch, and sends the headers on a refusal too; rgw-go
// counts from the upload's initiation, as S3 does, and sends them with the
// upload it created (docs/differences.md, "x-amz-abort-date counts from the
// upload's initiation").
func setAbortHeaders(h http.Header, r *op.Request, initiated time.Time) {
	c, ok := storedLifecycle(r)
	if !ok {
		return
	}
	date, id, ok := lifecycle.AbortHeader(c, r.Object.Name, initiated)
	if !ok {
		return
	}
	addHeader(h, "x-amz-abort-date", httpDate(date))
	if id != "" {
		addHeader(h, "x-amz-abort-rule-id", id)
	}
}

// storedLifecycle is the bucket's lifecycle configuration as
// s3_expiration_header and s3_multipart_abort_header decode it from the
// bucket's attrs
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2769-L2781 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2901-L2913 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2828-L2840 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2965-L2977 at v20.2.4).
// ok is false for a bucket without one and for an attr that does not
// decode, for which each sends no header.
func storedLifecycle(r *op.Request) (lifecycle.Configuration, bool) {
	b, ok := r.BucketRec.Attrs[op.AttrLC]
	if !ok {
		return lifecycle.Configuration{}, false
	}
	c, err := lifecycle.Decode(b)
	return c, err == nil
}

// storedTags is the tag set rgw_cond_decode_objtags leaves in s->tagset for
// b, an object's tags attr, which is nil for an object without one
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2198-L2212 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2433-L2447 at v20.2.4):
// RGWObjTags::decode's, whose failure it catches. When the binary form and
// the text fallback both fail, the set keeps the tags set_from_string added
// before the item it refused
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_tag.h#L32-L62 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_tag.cc#L36-L57, the
// same at v20.2.4).
func storedTags(b []byte) tags.Set {
	d := denc.NewDecoder(b)
	if set := tags.Decode(d); d.Err() == nil {
		return set
	}
	// set_from_string refuses every item past MaxObjectTags tags, so the
	// items after that many and one more are never read.
	items := strings.SplitN(string(bytes.TrimRight(b, "\x00")), "&", tags.MaxObjectTags+2)
	var kept tags.Set
	for k := 1; k <= min(len(items), tags.MaxObjectTags+1); k++ {
		set, err := tags.ParseHeader(strings.Join(items[:k], "&"), tags.MaxObjectTags)
		if err != nil {
			break
		}
		kept = set
	}
	return kept
}
