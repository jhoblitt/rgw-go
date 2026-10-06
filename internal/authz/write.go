package authz

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// UserResolver implements acl.Resolver over the user and account stores, as
// read_owner_display_name and read_aclowner_by_email look grantees up
// (rgw_acl_s3.cc:300-337 at v19.2.6 and v20.2.4). Accounts is
// op.AccountStore in every caller; when nil, an account owner or grantee is
// a miss, as radosgw reports an unknown id. Only a lookup that finds nothing
// is a miss. Any other failure, an account lookup the driver does not
// implement among them, is the S3 error it maps to, carrying neither the
// store's text nor the grantee, which may be a request header's value.
type UserResolver struct {
	Users    op.UserStore
	Accounts AccountLookup
}

// AccountLookup is what UserResolver reads of the account store;
// op.AccountStore satisfies it.
type AccountLookup interface {
	AccountName(ctx context.Context, id string) (string, error)
	GetAccountByEmail(ctx context.Context, email string) (*op.AccountRecord, error)
}

var _ acl.Resolver = UserResolver{}

// DisplayName is a user's display name or an account's name;
// acl.ErrNoSuchOwner when the store has neither.
func (u UserResolver) DisplayName(ctx context.Context, owner meta.Owner) (string, error) {
	if owner.User != nil {
		rec, err := u.Users.GetUser(ctx, *owner.User)
		switch {
		case errors.Is(err, op.ErrNoSuchUser):
			return "", acl.ErrNoSuchOwner
		case err != nil:
			return "", lookupFailed("user", err)
		}
		return rec.Info.DisplayName, nil
	}
	if u.Accounts == nil {
		return "", acl.ErrNoSuchOwner
	}
	name, err := u.Accounts.AccountName(ctx, owner.Account)
	switch {
	case errors.Is(err, op.ErrNoSuchEntity):
		return "", acl.ErrNoSuchOwner
	case err != nil:
		return "", lookupFailed("account", err)
	}
	return name, nil
}

// OwnerByEmail is the user the email index names, or else the account, with
// its name: users and accounts share the index; acl.ErrUnresolvableEmail when
// it names neither.
func (u UserResolver) OwnerByEmail(ctx context.Context, email string) (acl.Owner, error) {
	rec, err := u.Users.GetUserByEmail(ctx, email)
	switch {
	case err == nil:
		return acl.Owner{ID: rec.Info.UserID.String(), DisplayName: rec.Info.DisplayName}, nil
	case !errors.Is(err, op.ErrNoSuchUser):
		return acl.Owner{}, lookupFailed("user email", err)
	case u.Accounts == nil:
		return acl.Owner{}, acl.ErrUnresolvableEmail
	}
	acct, err := u.Accounts.GetAccountByEmail(ctx, email)
	switch {
	case errors.Is(err, op.ErrNoSuchEntity):
		return acl.Owner{}, acl.ErrUnresolvableEmail
	case err != nil:
		return acl.Owner{}, lookupFailed("account email", err)
	}
	return acl.Owner{ID: acct.Info.ID, DisplayName: acct.Info.Name}, nil
}

// lookupFailed is the S3 error err maps to, without its message, described
// by the lookup that failed.
func lookupFailed(lookup string, err error) error {
	return fmt.Errorf("%w: the grantee's %s lookup failed", op.AsError(err).WithMessage(""), lookup)
}

// BuildACL is RGWPutACLs::execute's derivation and validation of the new
// policy for a bucket, object false, or an object (rgw_op.cc:5821-5910 at
// v19.2.6, :6467-6556 at v20.2.4). A canned ACL with a body is
// acl.ErrInvalid. A canned ACL or a grant header builds the policy as
// create_s3_policy does for existing's owner, a bucket's canned ACL ignored
// when its name holds "bucket" (get_policy_from_state, rgw_rest_s3.cc:3637-3647
// at v19.2.6, :3920-3930 at v20.2.4); otherwise the body parses through
// acl.ParseS3XML. A new owner, when existing has one, is
// acl.ErrOwnerMismatch; more grants than rgw_acl_grants_max_num is
// acl.ErrTooManyGrants in an *acl.ParseError carrying radosgw's message; a
// public policy on a bucket whose public-access block sets BlockPublicAcls
// is op.ErrAccessDenied. A block that does not decode blocks, where radosgw
// takes it for none (docs/exclusions.md). Its errors are returned as acl
// makes them, some holding a request header's text, so a caller maps them
// through ErrorFor before rendering.
func (e *Evaluator) BuildACL(ctx context.Context, r *op.Request, res acl.Resolver, existing acl.Policy, body []byte, object bool) (acl.Policy, error) {
	canned, _ := op.HeaderValue(r.Header, "X-Amz-Acl")
	if canned != "" && len(body) > 0 {
		return acl.Policy{}, acl.ErrInvalid
	}
	var (
		p   acl.Policy
		err error
	)
	if canned != "" || hasGrantHeader(r.Header) {
		if !object && strings.Contains(canned, "bucket") {
			canned = ""
		}
		var bacl acl.Policy
		if r.BucketRec != nil {
			if bacl, err = op.BucketACLFor(r.BucketRec); err != nil {
				return acl.Policy{}, err
			}
		}
		p, err = createS3Policy(ctx, r, res, canned, existing.Owner, bacl.Owner)
	} else {
		p, err = acl.ParseS3XML(ctx, res, body)
	}
	if err != nil {
		return acl.Policy{}, err
	}
	if !ownerEmpty(existing.Owner.ID) && !sameOwner(existing.Owner.ID, p.Owner.ID) {
		return acl.Policy{}, acl.ErrOwnerMismatch
	}
	maxGrants := e.cfg.ACLGrantsMaxNum
	// ConfigFrom replaces a negative value, but a Config built without it
	// may still hold one.
	if maxGrants < 0 {
		maxGrants = aclGrantsMaxNum
	}
	if len(p.ACL.Grants) > maxGrants {
		return acl.Policy{}, &acl.ParseError{
			Message: "The request is rejected, because the acl grants number you requested is larger than the maximum " +
				strconv.Itoa(maxGrants) + " grants allowed in an acl.",
			Err: acl.ErrTooManyGrants,
		}
	}
	if p.IsPublic() && blocks(r.BucketRec, func(b *acl.PublicAccessBlock) bool { return b.BlockPublicACLs }) {
		return acl.Policy{}, fmt.Errorf("%w: the bucket blocks public acls", op.ErrAccessDenied)
	}
	return p, nil
}

// DefaultACL is create_s3_policy (rgw_rest_s3.cc:2408-2422 at v19.2.6,
// :2524-2538 at v20.2.4), the new ACL of CreateBucket, PutObject,
// InitMultipart and CopyObject: a grant header beside a canned ACL is
// op.ErrInvalidRequest; grant headers alone build through acl.FromHeaders for
// owner; otherwise acl.Canned with the X-Amz-Acl value, private when it is
// absent or empty, bucketOwner holding the bucket-owner grants and the
// policy of an anonymous owner. Its errors are returned as acl makes them,
// some holding a request header's text, so a caller maps them through
// ErrorFor before rendering.
func DefaultACL(ctx context.Context, r *op.Request, res acl.Resolver, owner, bucketOwner acl.Owner) (acl.Policy, error) {
	canned, _ := op.HeaderValue(r.Header, "X-Amz-Acl")
	return createS3Policy(ctx, r, res, canned, owner, bucketOwner)
}

// BuildDefaultACL is DefaultACL for an op that reaches it through the
// evaluator; its errors too go through ErrorFor before rendering.
func (e *Evaluator) BuildDefaultACL(ctx context.Context, r *op.Request, res acl.Resolver, owner, bucketOwner acl.Owner) (acl.Policy, error) {
	return DefaultACL(ctx, r, res, owner, bucketOwner)
}

// createS3Policy is create_s3_policy for the canned ACL canned. Each header
// is read by its last value, as RGWEnv::set keeps the last of a repeated
// header (rgw_env.cc:22-25 at v19.2.6 and v20.2.4).
func createS3Policy(ctx context.Context, r *op.Request, res acl.Resolver, canned string, owner, bucketOwner acl.Owner) (acl.Policy, error) {
	if !hasGrantHeader(r.Header) {
		return acl.Canned(owner, bucketOwner, canned)
	}
	if canned != "" {
		return acl.Policy{}, op.ErrInvalidRequest
	}
	grant := func(name string) string {
		v, _ := op.HeaderValue(r.Header, name)
		return v
	}
	return acl.FromHeaders(ctx, res, owner, acl.GrantHeaders{
		Read:        grant("X-Amz-Grant-Read"),
		Write:       grant("X-Amz-Grant-Write"),
		ReadACP:     grant("X-Amz-Grant-Read-Acp"),
		WriteACP:    grant("X-Amz-Grant-Write-Acp"),
		FullControl: grant("X-Amz-Grant-Full-Control"),
	})
}

// grantHeaderPrefix is the prefix of s->has_acl_header's headers,
// exists_prefix("HTTP_X_AMZ_GRANT") (rgw_rest_s3.cc:5024 at v19.2.6, :5584 at
// v20.2.4).
const grantHeaderPrefix = "X-Amz-Grant"

// hasGrantHeader reports whether h holds a header whose name starts with
// x-amz-grant in any case.
func hasGrantHeader(h http.Header) bool {
	for k := range h {
		if len(k) >= len(grantHeaderPrefix) && strings.EqualFold(k[:len(grantHeaderPrefix)], grantHeaderPrefix) {
			return true
		}
	}
	return false
}

// ownerEmpty is ACLOwner::empty (rgw_acl.cc:249-255 at v19.2.6 and
// v20.2.4): a user owner without an id, or an empty account id.
func ownerEmpty(id string) bool {
	o := meta.ParseOwner(id)
	if o.User != nil {
		return o.User.ID == ""
	}
	return o.Account == ""
}

// sameOwner is rgw_owner's equality for two ids in their to_string form.
func sameOwner(a, b string) bool {
	return meta.ParseOwner(a).String() == meta.ParseOwner(b).String()
}

// blocks reports whether the public-access block of rec, the request's
// bucket, whose block radosgw holds as s->bucket_access_conf, sets the flag
// set reads. A block that does not decode blocks.
func blocks(rec *op.BucketRecord, set func(*acl.PublicAccessBlock) bool) bool {
	b, err := publicAccess(rec)
	if err != nil {
		return true
	}
	return b != nil && set(b)
}

// ParseBucketPolicy is RGWPutBucketPolicy::execute's parse (rgw_op.cc:8098-8120
// at v19.2.6, :9024-9051 at v20.2.4): the body as a policy of the request's
// bucket's tenant, refusing the principals radosgw does not support when
// rgw_policy_reject_invalid_principals is set, under the cluster's release.
// A body that does not parse is the *policy.ParseError; a public policy on a
// bucket whose public-access block sets BlockPublicPolicy, judged by the
// release's is_public, is op.ErrAccessDenied, a block that does not decode
// blocking. The attr to store is the policy's Text. A caller maps its errors
// through ErrorFor before rendering.
func (e *Evaluator) ParseBucketPolicy(r *op.Request, body []byte) (*policy.Policy, error) {
	tenant := r.Tenant
	p, err := policy.Parse(string(body), policy.ParseOptions{
		Tenant: &tenant, RejectInvalidPrincipals: e.cfg.RejectInvalidPrincipals, Release: e.cfg.Release,
	})
	if err != nil {
		return nil, err
	}
	if p.IsPublic(e.sem) && blocks(r.BucketRec, func(b *acl.PublicAccessBlock) bool { return b.BlockPublicPolicy }) {
		return nil, fmt.Errorf("%w: the bucket blocks public policies", op.ErrAccessDenied)
	}
	return p, nil
}
