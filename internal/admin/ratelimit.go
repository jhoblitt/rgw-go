package admin

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// newRateLimitHandlers are RGWHandler_Ratelimit's routes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L344-L351 at v19.2.6
// and v20.2.4).
func newRateLimitHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"get_ratelimit_info": getRateLimitInfo,
		"put_ratelimit_info": putRateLimitInfo,
	}
}

// trueOrFalse is the check the ratelimit ops make of global and enabled
// before get_bool reads them: present, the value as RGWHTTPArgs::get holds
// it must be "true" or "false" in any case, boost::iequals
// (rgw_rest_ratelimit.cc:25-35 and :210-229).
func trueOrFalse(a Args, name string) (v, present bool, err error) {
	s, ok := a.Get(name)
	if !ok {
		return false, false, nil
	}
	switch lowerASCII(s) {
	case "true":
		return true, true, nil
	case "false":
		return false, true, nil
	}
	return false, true, fmt.Errorf("%w: %s is not true or false", op.ErrInvalidArgument, name)
}

// getRateLimitInfo is RGWOp_Ratelimit_Info::execute's argument handling
// (rgw_rest_ratelimit.cc:13-35): uid, ratelimit-scope, bucket and tenant
// through get_string, and a global that is not true or false refused after
// the cap check. Each branch starts the flusher before it dumps, so the
// body is WriteBody's: a bucket's or user's limits inside a section of the
// same name, as encode_json nests them in it, or the period config's three
// rate limits (:59-63, :95-99, :109-115).
func getRateLimitInfo(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := &op.GetRateLimit{}
	o.UID, _ = a.String("uid", "")
	o.Scope, _ = a.String("ratelimit-scope", "")
	o.Bucket, _ = a.String("bucket", "")
	o.Tenant, _ = a.String("tenant", "")
	var argErr error
	o.Global, _, argErr = trueOrFalse(a, "global")
	if err := runChecked(ctx, o, r, argErr); err != nil {
		return err
	}
	rel := r.Env.Zone.Release()
	return WriteBody(w, r, q, func(f formatter.Formatter) {
		limit := func(name string, l meta.RateLimitInfo) {
			f.OpenObjectSection(name)
			l.Dump(f, rel)
			f.CloseSection()
		}
		switch {
		case o.Global:
			f.OpenObjectSection("period_config")
			limit("bucket_ratelimit", o.Period.BucketRateLimit)
			limit("user_ratelimit", o.Period.UserRateLimit)
			limit("anonymous_ratelimit", o.Period.AnonRateLimit)
			f.CloseSection()
		case o.Scope == "bucket":
			f.OpenObjectSection("bucket_ratelimit")
			limit("bucket_ratelimit", o.Result)
			f.CloseSection()
		default:
			f.OpenObjectSection("user_ratelimit")
			limit("user_ratelimit", o.Result)
			f.CloseSection()
		}
	})
}

// putRateLimitInfo is RGWOp_Ratelimit_Set::execute's argument handling
// (rgw_rest_ratelimit.cc:197-229): the four limits through get_int64, any
// that does not parse refused first, then an enabled, then a global, that
// is not true or false, each refusal after the cap check. It sends no body.
func putRateLimitInfo(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := &op.SetRateLimit{}
	o.UID, _ = a.String("uid", "")
	o.Scope, _ = a.String("ratelimit-scope", "")
	o.Bucket, _ = a.String("bucket", "")
	o.Tenant, _ = a.String("tenant", "")
	var argErr error
	for _, l := range []struct {
		name string
		dst  **int64
	}{
		{"max-read-ops", &o.MaxReadOps},
		{"max-write-ops", &o.MaxWriteOps},
		{"max-read-bytes", &o.MaxReadBytes},
		{"max-write-bytes", &o.MaxWriteBytes},
	} {
		v, present, err := a.Int64(l.name, 0)
		if err != nil && argErr == nil {
			argErr = fmt.Errorf("%w: %s", err, l.name)
		}
		if present && err == nil {
			*l.dst = &v
		}
	}
	enabled, haveEnabled, err := trueOrFalse(a, "enabled")
	if argErr == nil {
		argErr = err
	}
	if haveEnabled {
		o.Enabled = &enabled
	}
	o.Global, _, err = trueOrFalse(a, "global")
	if argErr == nil {
		argErr = err
	}
	if err := runChecked(ctx, o, r, argErr); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}
