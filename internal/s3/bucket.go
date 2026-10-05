package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// bucketHandlers is the bucket-scope routes; each entry binds to bucket scope.
func bucketHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"list_bucket":         listObjects,
		"list_bucket_v2":      listObjectsV2,
		"create_bucket":       createBucket,
		"delete_bucket":       deleteBucket,
		"stat_bucket":         statBucket,
		"get_bucket_location": getBucketLocation,
	}
}

// The bucket routes are rgw_rest_s3.cc's RGWCreateBucket_ObjStore_S3,
// RGWDeleteBucket_ObjStore_S3, RGWStatBucket_ObjStore_S3 and
// RGWGetBucketLocation_ObjStore_S3. A bare line number is v19.2.6's
// rgw_rest_s3.cc.

// defaultMaxPutParamSize is rgw_max_put_param_size's default, 1 MiB
// (common/options/rgw.yaml.in:138-145 at v19.2.6, :144-151 at v20.2.4).
const defaultMaxPutParamSize = 1 << 20

// defaultMaxDynamicShards is rgw_max_dynamic_shards' default
// (common/options/rgw.yaml.in:3470-3483 at v20.2.4).
const defaultMaxDynamicShards = 1999

// createBucket is RGWCreateBucket_ObjStore_S3: get_params runs inside the
// op's Execute, and a success, a bucket that existed for its owner too, is
// 200 with no body (send_response, :2544-2555; v20.2.4 :2705-2716).
func createBucket(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.CreateBucket{
		Params: func(ctx context.Context, o *op.CreateBucket) error { return createBucketParams(ctx, r, o) },
	}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusOK)
	return nil
}

// writeEmpty answers status with no body and the Content-Length: 0 radosgw's
// frontend completes such a response with, without Accept-Ranges and without
// a type (end_header, rgw_rest.cc:611-619 at v19.2.6, :616-624 at v20.2.4).
// net/http sends no length with a 204.
func writeEmpty(w http.ResponseWriter, r *op.Request, status int) {
	SetCommonHeaders(w, r)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(status)
}

// createBucketParams is RGWCreateBucket_ObjStore_S3::get_params (:2473-2542;
// v20.2.4 :2593-2703): the name checked unless the request is a system
// user's, the ACL built from the headers, the body read and parsed, a
// "zonegroup:placement" constraint split, x-amz-storage-class taken as the
// placement's class, and the object-lock header carried for the op to
// check. Each header is read by its last value, as RGWEnv::set keeps the
// last of a repeated header (rgw_env.cc:22-25 at both tags).
func createBucketParams(ctx context.Context, r *op.Request, o *op.CreateBucket) error {
	if !r.Identity.System {
		if err := ValidBucketName(r.Bucket, confBool(r, "rgw_relaxed_s3_bucket_names")); err != nil {
			return err
		}
	}
	policy, err := createS3Policy(ctx, r)
	if err != nil {
		return err
	}
	o.ACL = policy
	body, err := readParamBody(r)
	if err != nil {
		return err
	}
	lc := ""
	if len(body) > 0 {
		if lc, o.BucketIndex, err = parseCreateBucketConfiguration(r, body); err != nil {
			return err
		}
	}
	class, _ := op.HeaderValue(r.Header, "x-amz-storage-class")
	if name, placement, ok := strings.Cut(lc, ":"); ok {
		o.Placement = meta.PlacementRule{Name: placement, StorageClass: class}
		lc = name
	} else {
		o.Placement.StorageClass = class
	}
	o.LocationConstraint = lc
	o.ObjectLock, o.HasObjectLock = op.HeaderValue(r.Header, "x-amz-bucket-object-lock-enabled")
	return nil
}

// readParamBody is read_all_input without chunked input
// (rgw_rest.cc:1537-1578 at v19.2.6): a body without a Content-Length is
// not read, which get_params passes over, and one longer than
// rgw_max_put_param_size is InvalidRange unread. Otherwise the body is read
// to its end through what authentication left in r.Body, so a payload that
// fails its check fails the request with that check's error before the body
// is used; a body that ends short of its length is RequestTimeout.
func readParamBody(r *op.Request) ([]byte, error) {
	if r.Body == nil || r.ContentLength <= 0 {
		return nil, nil
	}
	maxSize := uint64(defaultMaxPutParamSize)
	if r.Env.Conf != nil {
		if v, err := r.Env.Conf.Size("rgw_max_put_param_size"); err == nil {
			maxSize = v
		}
	}
	if uint64(r.ContentLength) > maxSize {
		return nil, op.ErrInvalidRange
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(min(maxSize, uint64(r.ContentLength)))+1)) //nolint:gosec // at most the positive ContentLength
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF):
		return nil, op.ErrRequestTimeout
	case err != nil:
		return nil, err
	case uint64(len(body)) > maxSize:
		return nil, op.ErrInvalidRange
	}
	return body, nil
}

// parseCreateBucketConfiguration reads body as RGWCreateBucketParser does,
// returning the LocationConstraint's text and whether a valid BucketIndex
// asks for an index, which the op refuses as NotImplemented: rgw-go does
// not yet create a bucket with a requested index (docs/exclusions.md). A
// body radosgw's XML parser refuses is InvalidArgument. On Squid the root
// must be CreateBucketConfiguration holding a LocationConstraint
// (:2500-2520); on Tentacle the LocationConstraint may be absent, and a
// BucketIndex is checked as radosgw checks it.
func parseCreateBucketConfiguration(r *op.Request, body []byte) (lc string, index bool, err error) {
	root, err := xmltext.Parse(body)
	if err != nil {
		return "", false, op.ErrInvalidArgument
	}
	tentacle := r.Env.Zone.Release() >= denc.Tentacle
	if root.Name != "CreateBucketConfiguration" {
		if tentacle {
			return "", false, op.ErrInvalidArgument.WithMessage("Missing required element CreateBucketConfiguration")
		}
		return "", false, op.ErrInvalidArgument
	}
	constraint := root.FindFirst("LocationConstraint")
	if !tentacle {
		if constraint == nil {
			return "", false, op.ErrInvalidArgument
		}
		return constraint.Text, false, nil
	}
	if bi := root.FindFirst("BucketIndex"); bi != nil {
		if err := checkBucketIndex(r, bi); err != nil {
			return "", false, err
		}
		index = true
	}
	if constraint != nil {
		lc = constraint.Text
	}
	return lc, index, nil
}

// checkBucketIndex is Tentacle's BucketIndex checks (v20.2.4
// rgw_rest_s3.cc:2651-2684), each refusal InvalidArgument with radosgw's
// message: a Type, Normal or Indexless in any case; a NumShards only for a
// Normal index, an unsigned 32-bit number from 1 to rgw_max_dynamic_shards.
func checkBucketIndex(r *op.Request, index *xmltext.Element) error {
	typ := index.FindFirst("Type")
	if typ == nil {
		return op.ErrInvalidArgument.WithMessage("Missing required element Type in BucketIndex")
	}
	normal := strings.EqualFold(typ.Text, "Normal")
	if !normal && !strings.EqualFold(typ.Text, "Indexless") {
		return op.ErrInvalidArgument.WithMessage("Unknown Type in BucketIndex")
	}
	shards := index.FindFirst("NumShards")
	if shards == nil {
		return nil
	}
	if !normal {
		return op.ErrInvalidArgument.WithMessage("NumShards requires Type to be Normal")
	}
	n, err := strconv.ParseUint(shards.Text, 10, 32)
	if err != nil {
		return op.ErrInvalidArgument.WithMessage("Failed to parse integer NumShards in BucketIndex")
	}
	if n == 0 {
		return op.ErrInvalidArgument.WithMessage("NumShards must be greater than 0")
	}
	limit := uint64(defaultMaxDynamicShards)
	if r.Env.Conf != nil {
		if v, err := r.Env.Conf.Uint64("rgw_max_dynamic_shards"); err == nil {
			limit = v
		}
	}
	if n > limit {
		return op.ErrInvalidArgument.WithMessage(fmt.Sprintf("NumShards cannot exceed %d", limit))
	}
	return nil
}

// confBool is a boolean option, false when it cannot be read.
func confBool(r *op.Request, name string) bool {
	if r.Env.Conf == nil {
		return false
	}
	v, err := r.Env.Conf.Bool(name)
	return err == nil && v
}

// createS3Policy is create_s3_policy (:2408-2422; v20.2.4 :2524-2538) for
// the requester's ACL owner: with any x-amz-grant header, the grants the
// headers name, which a canned ACL beside them makes InvalidRequest;
// otherwise the canned ACL, private when x-amz-acl is absent, whose
// bucket-owner grants name s->bucket_owner, which a create has not set yet.
// An unknown canned ACL or a grant radosgw cannot parse is InvalidArgument,
// and a grantee that does not exist is radosgw's -ENOENT, NoSuchKey.
func createS3Policy(ctx context.Context, r *op.Request) (acl.Policy, error) {
	owner := aclOwner(r.Identity)
	canned, _ := op.HeaderValue(r.Header, "x-amz-acl")
	var (
		p   acl.Policy
		err error
	)
	if hasGrantHeader(r.Header) {
		if canned != "" {
			return acl.Policy{}, op.ErrInvalidRequest
		}
		grant := func(name string) string {
			v, _ := op.HeaderValue(r.Header, name)
			return v
		}
		p, err = acl.FromHeaders(ctx, userResolver(r), owner, acl.GrantHeaders{
			Read:        grant("x-amz-grant-read"),
			Write:       grant("x-amz-grant-write"),
			ReadACP:     grant("x-amz-grant-read-acp"),
			WriteACP:    grant("x-amz-grant-write-acp"),
			FullControl: grant("x-amz-grant-full-control"),
		})
	} else {
		p, err = acl.Canned(owner, acl.Owner{}, canned)
	}
	switch {
	case errors.Is(err, acl.ErrInvalid):
		return acl.Policy{}, fmt.Errorf("%w: %w", op.ErrInvalidArgument, err)
	case errors.Is(err, acl.ErrGranteeNotFound):
		return acl.Policy{}, fmt.Errorf("%w: %w", op.ErrNoSuchKey, err)
	case err != nil:
		return acl.Policy{}, err
	}
	return p, nil
}

// hasGrantHeader is s->has_acl_header, any header whose name starts with
// x-amz-grant (rgw_rest_s3.cc:5024 at v19.2.6, :5584 at v20.2.4).
func hasGrantHeader(h http.Header) bool {
	for k := range h {
		if len(k) >= len("x-amz-grant") && strings.EqualFold(k[:len("x-amz-grant")], "x-amz-grant") {
			return true
		}
	}
	return false
}

// aclOwner is get_aclowner (rgw_auth.cc:1019-1030 at v19.2.6, :1039-1050 at
// v20.2.4): an account's user's buckets belong to its account, named by the
// account's name, and any other user's to the user, named by its display
// name.
func aclOwner(id op.Identity) acl.Owner {
	if id.Account != nil {
		return acl.Owner{ID: id.Account.ID, DisplayName: id.Account.Name}
	}
	if id.User != nil {
		return acl.Owner{ID: id.User.UserID.String(), DisplayName: id.User.DisplayName}
	}
	return acl.Owner{}
}

// userResolver is the acl.Resolver grant headers and ACL documents resolve
// their grantees through, over r's user and account stores. The resolver is
// local until the authorizer's own resolver lands.
func userResolver(r *op.Request) acl.Resolver {
	return storeResolver{users: r.Env.Users, accounts: r.Env.Accounts}
}

// storeResolver is read_owner_display_name and read_aclowner_by_email
// (rgw_acl_s3.cc:300-337 at v19.2.6 and v20.2.4) over the stores: an email
// names a user, or else an account, through the email index they share.
type storeResolver struct {
	users    op.UserStore
	accounts op.AccountStore
}

// DisplayName is a user's display name or an account's name.
func (s storeResolver) DisplayName(ctx context.Context, owner meta.Owner) (string, error) {
	if owner.User != nil {
		rec, err := s.users.GetUser(ctx, *owner.User)
		if err != nil {
			return "", ownerMiss(err)
		}
		return rec.Info.DisplayName, nil
	}
	rec, err := s.accounts.GetAccount(ctx, owner.Account)
	if err != nil {
		return "", ownerMiss(err)
	}
	return rec.Info.Name, nil
}

// OwnerByEmail is the user or account an email names, with its name.
func (s storeResolver) OwnerByEmail(ctx context.Context, email string) (acl.Owner, error) {
	user, err := s.users.GetUserByEmail(ctx, email)
	switch {
	case err == nil:
		return acl.Owner{ID: user.Info.UserID.String(), DisplayName: user.Info.DisplayName}, nil
	case !errors.Is(err, op.ErrNoSuchUser):
		return acl.Owner{}, err
	}
	acct, err := s.accounts.GetAccountByEmail(ctx, email)
	switch {
	case errors.Is(err, op.ErrNoSuchEntity):
		return acl.Owner{}, fmt.Errorf("%w: %w", acl.ErrUnresolvableEmail, err)
	case err != nil:
		return acl.Owner{}, err
	}
	return acl.Owner{ID: acct.Info.ID, DisplayName: acct.Info.Name}, nil
}

// ownerMiss marks a store's miss as acl.ErrNoSuchOwner and passes any other
// failure on.
func ownerMiss(err error) error {
	if errors.Is(err, op.ErrNoSuchUser) || errors.Is(err, op.ErrNoSuchEntity) {
		return fmt.Errorf("%w: %w", acl.ErrNoSuchOwner, err)
	}
	return err
}

// deleteBucket is RGWDeleteBucket_ObjStore_S3: 204 with no body
// (send_response, :2547-2556 at v19.2.6, :2731-2740 at v20.2.4).
func deleteBucket(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.DeleteBucket{}, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusNoContent)
	return nil
}

// statBucket is RGWStatBucket_ObjStore_S3, the bucket HEAD: 200 with the
// bucket's counts and, for its owner, the quotas, as headers. Squid always
// reads and sends the counts, and sends the requesting user's quota and
// bucket limit and the bucket's quota to the owner (:2377-2406). Tentacle
// reads and sends the counts only with read-stats, and sends the owner the
// bucket limit, the user quota only when it is enabled and the bucket quota
// only when it is (v20.2.4 :2480-2522).
func statBucket(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	tentacle := r.Env.Zone.Release() >= denc.Tentacle
	o := &op.StatBucket{ReadStats: !tentacle || r.Query.Has("read-stats")}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	h := w.Header()
	if o.ReadStats {
		h.Set("X-RGW-Object-Count", strconv.FormatUint(o.Stats.NumObjects, 10))
		h.Set("X-RGW-Bytes-Used", strconv.FormatUint(o.Stats.Size, 10))
	}
	if u := r.Identity.User; u != nil && isOwnerOf(r.Identity, r.BucketRec.Info.Owner) {
		bq := r.BucketRec.Info.Quota
		h.Set("X-RGW-Quota-Max-Buckets", strconv.FormatInt(int64(u.MaxBuckets), 10))
		if !tentacle || u.UserQuota.Enabled {
			h.Set("X-RGW-Quota-User-Size", strconv.FormatInt(u.UserQuota.MaxSize, 10))
			h.Set("X-RGW-Quota-User-Objects", strconv.FormatInt(u.UserQuota.MaxObjects, 10))
		}
		if !tentacle || bq.Enabled {
			h.Set("X-RGW-Quota-Bucket-Size", strconv.FormatInt(bq.MaxSize, 10))
			h.Set("X-RGW-Quota-Bucket-Objects", strconv.FormatInt(bq.MaxObjects, 10))
		}
	}
	writeEmpty(w, r, http.StatusOK)
	return nil
}

// getBucketLocation is RGWGetBucketLocation_ObjStore_S3::send_response
// (:2129-2150; v20.2.4 :2230-2253): the XML declaration and a
// LocationConstraint in the S3 namespace holding the api name, escaped as
// XMLFormatter escapes it. Its length is the one the frontend adds, without
// Accept-Ranges. Squid sends no Content-Type, its end_header running before
// the formatter holds anything; Tentacle sends application/xml and the
// bucket's placement name in x-rgw-bucket-placement-target.
func getBucketLocation(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetBucketLocation{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	body := xmlHeader + `<LocationConstraint xmlns="` + xmlnsS3 + `">` + xmltext.Escape(o.APIName) + `</LocationConstraint>`
	SetCommonHeaders(w, r)
	h := w.Header()
	if r.Env.Zone.Release() >= denc.Tentacle {
		h.Set("x-rgw-bucket-placement-target", r.BucketRec.Info.PlacementRule.Name)
		h.Set("Content-Type", "application/xml")
	} else {
		h["Content-Type"] = nil
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	// A failed write means the client is gone, with no one left to tell.
	_, _ = io.WriteString(w, body) //nolint:errcheck // see above
	return nil
}
