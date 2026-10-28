package op

import (
	"bytes"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/cors"
)

// verifyCORSMatch is RGWOp::verify_cors_match on radosgw's main branch
// (https://github.com/ceph/ceph/blob/292995374d9/src/rgw/rgw_op.cc#L2088-L2133 at 292995374d9),
// which rgw_process_authenticated calls once the op mask has passed and
// before verify_permission, so no admin override lifts its refusal
// (https://github.com/ceph/ceph/blob/292995374d9/src/rgw/rgw_process.cc#L289-L299 at 292995374d9).
// A request other than the preflight that carries a non-empty Origin, and
// whose loaded bucket has a CORS configuration, is refused with -EACCES,
// 403 AccessDenied, unless a rule allows that Origin for the request's own
// method: Access-Control-Request-Method decides only the response's
// headers, and no requested header chooses a rule. A configuration that
// does not decode is read_bucket_cors's 500, which main's check ignores to
// match against whatever the decode left. The read of a website's error
// document passes, as serve_errordoc makes no such check. Neither floor
// refuses at all (docs/exclusions.md, "A request whose Origin no CORS rule
// allows is refused").
func verifyCORSMatch(o Op, r *Request) error {
	if r.WebsiteErrorDoc || o.Name() == OptionsCORSName {
		return nil
	}
	origin, _ := HeaderValue(r.Header, "Origin")
	if origin == "" {
		return nil
	}
	c, err := r.BucketCORS()
	if err != nil || c == nil {
		return err
	}
	if c.Match(origin, r.Method, nil) == nil {
		return fmt.Errorf("%w: no cors rule of bucket %s allows %s from the request's origin", ErrAccessDenied, r.BucketRec.Info.Bucket.Name, r.Method)
	}
	return nil
}

// corsDecode is the configuration BucketCORS decoded from raw, the attr's
// bytes as the record held them; ok once it has decoded any.
type corsDecode struct {
	raw  []byte
	conf *cors.Configuration
	err  error
	ok   bool
}

// BucketCORS is the CORS configuration r.BucketRec holds, as bucketCORS
// reads it: nil for no record or no configuration, UnknownError for one that
// does not decode. It decodes the attr once while the record holds the same
// bytes, so Run's origin check and the response's CORS headers share one
// decode, and a write that replaces the attr is read anew.
func (r *Request) BucketCORS() (*cors.Configuration, error) {
	if r.BucketRec == nil {
		return nil, nil
	}
	raw, ok := r.BucketRec.Attrs[AttrCORS]
	if !ok {
		return nil, nil
	}
	if !r.cors.ok || !bytes.Equal(r.cors.raw, raw) {
		c, err := bucketCORS(r.BucketRec)
		r.cors = corsDecode{raw: raw, conf: c, err: err, ok: true}
	}
	return r.cors.conf, r.cors.err
}
