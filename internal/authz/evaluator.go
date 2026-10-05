package authz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// restrictSemantics judges whether a bucket policy is public for
// RestrictPublicBuckets, which rgw-go enforces on both releases as v20.2.4
// does (rgw_common.cc:1374-1380 and :1541-1547 at v20.2.4). Squid's is_public
// counts nearly every Allow statement, so its rule would turn the block into
// a refusal of almost every grant.
var restrictSemantics = policy.SemanticsFor(denc.Tentacle)

// Evaluator implements op.Authorizer: radosgw's verify_user_permission,
// verify_bucket_permission and verify_object_permission through the request
// state (rgw_common.cc:1250-1637 at v19.2.6, :1263-1699 at v20.2.4), preceded
// by the refusals radosgw makes before verify_permission, which it marks with
// op.BeforeVerify. The policy language follows the configured release; the
// checks outside it, the admin bypasses, x-amz-expected-bucket-owner and
// RestrictPublicBuckets, are v20.2.4's on both releases (docs/exclusions.md).
type Evaluator struct {
	cfg         Config
	sem         policy.Semantics
	groupWarned sync.Map // user id -> struct{}: IAM group members already logged
}

var _ op.Authorizer = (*Evaluator)(nil)

// New returns an Evaluator for cfg.
func New(cfg Config) *Evaluator {
	return &Evaluator{cfg: cfg, sem: policy.SemanticsFor(cfg.Release)}
}

// deniedAsGroupMember refuses an identity that belongs to an IAM group.
// radosgw loads each group's inline and managed policies with the user's
// own at authentication (load_account_and_policies, rgw_auth.cc:170-184 at
// v19.2.6 and v20.2.4) and evaluates them on every request, so a group's
// Deny refuses what the user's policies and the ACLs allow. With no group
// store to read them from, evaluating the rest could grant what radosgw
// denies; an admin identity still passes through op.Run's override, as it
// does after any policy denial (rgw_process.cc:228-239 at v20.2.4).
func (e *Evaluator) deniedAsGroupMember(ctx context.Context, id *op.Identity) error {
	if id.User == nil || len(id.User.GroupIDs) == 0 {
		return nil
	}
	uid := id.User.UserID.String()
	if _, logged := e.groupWarned.LoadOrStore(uid, struct{}{}); !logged {
		slog.WarnContext(ctx, "refusing an iam group member: group policies are not evaluated",
			slog.String("user", uid), slog.Int("groups", len(id.User.GroupIDs)))
	}
	return fmt.Errorf("%w: %s belongs to an iam group", op.ErrAccessDenied, uid)
}

// VerifyUser is verify_user_permission for an action on no bucket:
// ListAllMyBuckets (RGWListBuckets::verify_permission, rgw_op.cc:2489-2500 at
// v19.2.6, :2721-2732 at v20.2.4) and CreateBucket
// (RGWCreateBucket::verify_permission, :3215-3245 at v19.2.6, :3443-3472 at
// v20.2.4, whose max-buckets check is the op's).
func (e *Evaluator) VerifyUser(ctx context.Context, r *op.Request, a policy.Action) error {
	ids, err := e.identityPolicies(r)
	if err != nil {
		return e.decided(ctx, a, "identity policies do not decode", err)
	}
	if err := e.deniedAsGroupMember(ctx, &r.Identity); err != nil {
		return e.decided(ctx, a, "iam group member", err)
	}
	id := identity{id: &r.Identity}
	arn := policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceS3, Account: r.Identity.Tenant, Resource: "*"}
	if a == policy.S3CreateBucket {
		if id.IsAnonymous() {
			return e.decided(ctx, a, "anonymous", op.ErrAccessDenied)
		}
		arn = policy.BucketARN(r.Tenant, r.Bucket)
	}
	env := buildEnv(e.cfg, r, a, nil)
	effect, rule := e.evaluate(env, id, id.IdentityType() == meta.IdentityRoot, a, arn, nil, ids)
	switch effect {
	case policy.Deny:
		return e.decided(ctx, a, rule, op.ErrAccessDenied)
	case policy.Pass:
		// verify_user_permission makes a policy mandatory for an account
		// user (rgw_common.cc:1305-1308 at v19.2.6, :1318-1321 at v20.2.4);
		// verify_user_permission_no_policy refuses a role and finds no user
		// ACL for S3, which allows (:1281-1297, :1294-1310).
		switch {
		case r.Identity.Account != nil:
			return e.decided(ctx, a, "no policy allows an account user", op.ErrAccessDenied)
		case id.IdentityType() == meta.IdentityRole:
			return e.decided(ctx, a, "no policy allows a role", op.ErrAccessDenied)
		}
		rule = "no user acl"
	}
	if a == policy.S3CreateBucket && r.Identity.Tenant != r.Tenant && id.IdentityType() != meta.IdentityRole {
		return e.decided(ctx, a, "bucket in another tenant", op.ErrAccessDenied)
	}
	return e.decided(ctx, a, rule, nil)
}

// VerifyBucket is verify_bucket_permission(s, op) (rgw_common.cc:1461-1477 at
// v19.2.6, :1498-1514 at v20.2.4) against r.BucketRec for the key r names,
// after the refusals init_permissions makes on the request's bucket.
func (e *Evaluator) VerifyBucket(ctx context.Context, r *op.Request, a policy.Action, perm acl.Permission) error {
	ids, b, rule, err := e.requestPrelude(r)
	if err != nil {
		return e.decided(ctx, a, rule, err)
	}
	if err := e.deniedAsGroupMember(ctx, &r.Identity); err != nil {
		return e.decided(ctx, a, "iam group member", err)
	}
	return e.verifyBucket(ctx, r, a, perm, b, b.block, r.Object, ids, formBucket)
}

// VerifyObject is verify_object_permission(s, op) (rgw_common.cc:1625-1637 at
// v19.2.6, :1688-1699 at v20.2.4) against r.ObjState, after the refusals
// init_permissions and read_permissions make: those on the request's bucket,
// an object ACL that does not decode, and read_obj_policy's rule for a
// missing object (rgw_op.cc:385-447 at v19.2.6, :415-477 at v20.2.4).
func (e *Evaluator) VerifyObject(ctx context.Context, r *op.Request, a policy.Action, perm acl.Permission) error {
	ids, b, rule, err := e.requestPrelude(r)
	if err != nil {
		return e.decided(ctx, a, rule, err)
	}
	obj := r.ObjState
	if obj == nil || !obj.Exists {
		key := r.Object.Name
		if obj != nil {
			key = obj.Key.Name
		}
		return e.missingObject(ctx, r, a, b, b.block, key, ids, op.BeforeVerify)
	}
	oacl, err := op.ObjectACLFor(ctx, obj, b.rec)
	if err != nil {
		return e.decided(ctx, a, "object acl does not decode", op.BeforeVerify(err))
	}
	if err := e.deniedAsGroupMember(ctx, &r.Identity); err != nil {
		return e.decided(ctx, a, "iam group member", err)
	}
	return e.verifyObject(ctx, r, a, perm, b, b.block, obj, oacl, r.Object.Name, ids, formObject)
}

// VerifyBucketIn is VerifyBucket against an explicit bucket and the key its
// ARN carries, Name "" for the bucket ARN: CopyObject's source
// (RGWCopyObj::verify_permission, rgw_op.cc:5407-5460 at v19.2.6, :5973-6026
// at v20.2.4) and DeleteObjects' per-key check (:6818-6838 at v19.2.6,
// :7743-7775 at v20.2.4). The resource's inputs are bucket's: its ACL and
// owner, its policy parsed with its own tenant, and its public-access block,
// which applies with the request's bucket's. The request's bucket supplies
// requester pays and the x-amz-expected-bucket-owner comparison. A copy
// source's suspended bucket and undecodable ACL or block are refused
// unmarked, as radosgw reads a source inside verify_permission; a source
// policy that does not parse is refused marked, and for every requester.
func (e *Evaluator) VerifyBucketIn(ctx context.Context, r *op.Request, a policy.Action, perm acl.Permission, bucket *op.BucketRecord, key meta.ObjKey) error {
	ids, b, reqBlock, rule, err := e.explicitPrelude(r, bucket)
	if err != nil {
		return e.decided(ctx, a, rule, err)
	}
	if err := e.deniedAsGroupMember(ctx, &r.Identity); err != nil {
		return e.decided(ctx, a, "iam group member", err)
	}
	return e.verifyBucket(ctx, r, a, perm, b, reqBlock, key, ids, formBucketIn)
}

// VerifyObjectIn is VerifyObject against an explicit object in an explicit
// bucket, UploadPartCopy's source (RGWPutObj::verify_permission,
// rgw_op.cc:3920-3963 at v19.2.6, :4129-4172 at v20.2.4), with
// VerifyBucketIn's inputs; an object without an ACL of its own gets a default
// one owned by bucket's owner. A missing object takes read_obj_policy's rule
// on bucket, unmarked.
func (e *Evaluator) VerifyObjectIn(ctx context.Context, r *op.Request, a policy.Action, perm acl.Permission, bucket *op.BucketRecord, obj *op.ObjectState) error {
	ids, b, reqBlock, rule, err := e.explicitPrelude(r, bucket)
	if err != nil {
		return e.decided(ctx, a, rule, err)
	}
	if obj == nil || !obj.Exists {
		var key string
		if obj != nil {
			key = obj.Key.Name
		}
		return e.missingObject(ctx, r, a, b, reqBlock, key, ids, unmarked)
	}
	oacl, err := op.ObjectACLFor(ctx, obj, bucket)
	if err != nil {
		return e.decided(ctx, a, "object acl does not decode", err)
	}
	if err := e.deniedAsGroupMember(ctx, &r.Identity); err != nil {
		return e.decided(ctx, a, "iam group member", err)
	}
	return e.verifyObject(ctx, r, a, perm, b, reqBlock, obj, oacl, obj.Key.Name, ids, formObjectIn)
}

func unmarked(err error) error { return err }

// decided logs which rule decided a check and returns err, nil when allowed.
func (e *Evaluator) decided(ctx context.Context, a policy.Action, rule string, err error) error {
	slog.DebugContext(ctx, "authorization decided", slog.String("op", a.String()), slog.String("rule", rule))
	return err
}

// identityPolicies are the identity's policies, refused as authentication
// refuses them, before any check: radosgw loads the user's account and its
// policies at authentication (load_account_and_policies, rgw_auth.cc:135-187
// at v19.2.6 and v20.2.4) and fails it with -EPERM when one does not decode
// (:548-554 at v19.2.6, :563-569 at v20.2.4). A user of an account that is
// not loaded is refused too, rather than judged by the ACLs as a user
// outside any account would be.
func (e *Evaluator) identityPolicies(r *op.Request) ([]*policy.Policy, error) {
	if u := r.Identity.User; u != nil && u.AccountID != "" && r.Identity.Account == nil {
		return nil, op.BeforeVerify(fmt.Errorf("%w: account %s of %s is not loaded", op.ErrAccessDenied, u.AccountID, u.UserID))
	}
	ids, err := identityPolicies(&r.Identity, e.cfg.Release)
	if err != nil {
		return nil, op.BeforeVerify(fmt.Errorf("%w: %w", op.ErrAccessDenied, err))
	}
	return ids, nil
}

// requestPrelude is what VerifyBucket and VerifyObject read before the
// object: the identity's policies and the request's bucket, each refusal
// marked, with the rule that refused.
func (e *Evaluator) requestPrelude(r *op.Request) ([]*policy.Policy, bucketInputs, string, error) {
	ids, err := e.identityPolicies(r)
	if err != nil {
		return nil, bucketInputs{}, "identity refused", err
	}
	if r.BucketRec == nil {
		return nil, bucketInputs{}, "request names no bucket", op.ErrAccessDenied
	}
	b, err := e.requestBucket(r)
	if err != nil {
		return nil, bucketInputs{}, "request bucket refused", err
	}
	return ids, b, "", nil
}

// explicitPrelude is what VerifyBucketIn and VerifyObjectIn read before the
// object: the identity's policies, the explicit bucket, and the request's
// bucket's public-access block, which refuses unmarked when it does not
// decode.
func (e *Evaluator) explicitPrelude(r *op.Request, bucket *op.BucketRecord) ([]*policy.Policy, bucketInputs, *acl.PublicAccessBlock, string, error) {
	ids, err := e.identityPolicies(r)
	if err != nil {
		return nil, bucketInputs{}, nil, "identity refused", err
	}
	if bucket == nil {
		return nil, bucketInputs{}, nil, "no bucket", op.ErrAccessDenied
	}
	b, err := e.explicitBucket(r, bucket)
	if err != nil {
		return nil, bucketInputs{}, nil, "explicit bucket refused", err
	}
	reqBlock, err := publicAccess(r.BucketRec)
	if err != nil {
		return nil, bucketInputs{}, nil, "request bucket block does not decode", err
	}
	return ids, b, reqBlock, "", nil
}

// bucketInputs are what a check reads from the bucket that holds its
// resource: the record, its ACL, whose owner is radosgw's s->bucket_owner, its
// stored policy and its public-access block.
type bucketInputs struct {
	rec    *op.BucketRecord
	acl    acl.Policy
	policy *policy.Policy
	block  *acl.PublicAccessBlock
}

func suspended(rec *op.BucketRecord) bool { return rec.Info.Flags&meta.BucketSuspended != 0 }

// requestBucket reads r.BucketRec as rgw_build_bucket_policies does in
// init_permissions (rgw_op.cc:494-623 at v19.2.6, :524-653 at v20.2.4),
// marking each refusal. A policy that does not parse refuses all but an
// admin, and replaces a suspended bucket's or an undecodable ACL's refusal,
// as the policy's -EACCES overwrites read_bucket_policy's result
// (:598-615 at v19.2.6, :628-645 at v20.2.4); a public-access block that does
// not decode is refused the same way (docs/exclusions.md). For an admin the
// failed policy or block is none. A suspended bucket refuses all but an admin
// (read_bucket_policy, :366-370 at v19.2.6, :396-400 at v20.2.4).
func (e *Evaluator) requestBucket(r *op.Request) (bucketInputs, error) {
	rec := r.BucketRec
	admin := r.Identity.Admin
	bp, perr := bucketPolicy(rec, rec.Info.Bucket.Tenant, e.cfg.Release)
	block, berr := publicAccess(rec)
	if err := errors.Join(perr, berr); err != nil && !admin {
		return bucketInputs{}, op.BeforeVerify(fmt.Errorf("%w: %w", op.ErrAccessDenied, err))
	}
	if perr != nil {
		bp = nil
	}
	if berr != nil {
		block = nil
	}
	if suspended(rec) && !admin {
		return bucketInputs{}, op.BeforeVerify(fmt.Errorf("%w: bucket %s", op.ErrUserSuspended, rec.Info.Bucket.Name))
	}
	bacl, err := op.BucketACLFor(rec)
	if err != nil {
		return bucketInputs{}, op.BeforeVerify(err)
	}
	return bucketInputs{rec: rec, acl: bacl, policy: bp, block: block}, nil
}

// explicitBucket reads a copy source's bucket as read_obj_policy does inside
// verify_permission (rgw_op.cc:385-419 at v19.2.6, :415-449 at v20.2.4): a
// suspended bucket refuses all but an admin, unmarked. Its policy is parsed
// there with no handler, and radosgw's process terminates when it does not
// parse (docs/ceph-upstream-bugs.md, "radosgw terminates on a copy source or
// system request whose bucket policy does not parse"); rgw-go refuses the
// request for every requester instead, marked. An ACL or block that does not
// decode is refused unmarked.
func (e *Evaluator) explicitBucket(r *op.Request, rec *op.BucketRecord) (bucketInputs, error) {
	if suspended(rec) && !r.Identity.Admin {
		return bucketInputs{}, fmt.Errorf("%w: bucket %s", op.ErrUserSuspended, rec.Info.Bucket.Name)
	}
	bp, err := bucketPolicy(rec, rec.Info.Bucket.Tenant, e.cfg.Release)
	if err != nil {
		return bucketInputs{}, op.BeforeVerify(fmt.Errorf("%w: %w", op.ErrAccessDenied, err))
	}
	block, err := publicAccess(rec)
	if err != nil {
		return bucketInputs{}, err
	}
	bacl, err := op.BucketACLFor(rec)
	if err != nil {
		return bucketInputs{}, err
	}
	return bucketInputs{rec: rec, acl: bacl, policy: bp, block: block}, nil
}

// form is which Authorizer method a check comes from, which with the action
// selects the op whose tag keys it adds.
type form uint8

const (
	formBucket form = iota
	formBucketIn
	formObject
	formObjectIn
)

func (e *Evaluator) verifyBucket(ctx context.Context, r *op.Request, a policy.Action, perm acl.Permission,
	b bucketInputs, reqBlock *acl.PublicAccessBlock, key meta.ObjKey, ids []*policy.Policy, f form,
) error {
	bid := b.rec.Info.Bucket
	arn := policy.BucketARN(bid.Tenant, bid.Name)
	if key.Name != "" {
		arn = policy.ObjectARN(bid.Tenant, bid.Name, key.Name)
	}
	add, err := e.tagAdder(ctx, r, a, b, key, nil, ids, f)
	if err != nil {
		return e.decided(ctx, a, "tags unreadable", tagReadError(ctx, err))
	}
	c := check{
		a: a, perm: perm, arn: arn, ids: ids, bucket: b, reqBlock: reqBlock,
		env: buildEnv(e.cfg, r, a, add),
	}
	ok, rule := e.permit(r, &c)
	return e.decided(ctx, a, rule, denial(ok))
}

func (e *Evaluator) verifyObject(ctx context.Context, r *op.Request, a policy.Action, perm acl.Permission,
	b bucketInputs, reqBlock *acl.PublicAccessBlock, obj *op.ObjectState, oacl acl.Policy, key string,
	ids []*policy.Policy, f form,
) error {
	add, err := e.tagAdder(ctx, r, a, b, r.Object, obj, ids, f)
	if err != nil {
		return e.decided(ctx, a, "tags unreadable", tagReadError(ctx, err))
	}
	bid := b.rec.Info.Bucket
	c := check{
		a: a, perm: perm, arn: policy.ObjectARN(bid.Tenant, bid.Name, key), ids: ids,
		bucket: b, reqBlock: reqBlock, object: &oacl,
		env: buildEnv(e.cfg, r, a, add),
	}
	ok, rule := e.permit(r, &c)
	return e.decided(ctx, a, rule, denial(ok))
}

// tagReadError is the refusal of a check whose policies name tags it could
// not read: InternalError, a fault the client sees as one and may retry, and
// which op.Run never overrides, so that no requester, an admin included, is
// served past a tag-conditioned Deny that could not be evaluated. The cause
// is logged and described, never wrapped, so that a stat that failed with an
// S3 error, AccessDenied among them, is not taken for that error.
func tagReadError(ctx context.Context, err error) error {
	slog.WarnContext(ctx, "refusing a request whose policies need tags that cannot be read", slog.Any("error", err))
	return fmt.Errorf("%w: %v", op.ErrInternalError, err) //nolint:errorlint // the cause stays out of the chain
}

func denial(ok bool) error {
	if ok {
		return nil
	}
	return op.ErrAccessDenied
}

// missingObject is read_obj_policy's rule for an object that does not exist
// (rgw_op.cc:421-447 at v19.2.6, :451-477 at v20.2.4): NoSuchKey to an admin,
// and otherwise NoSuchKey only when the requester may list the bucket under
// the key as s3:prefix, evaluated in rgw_build_iam_environment's keys and that
// one, so that a requester who may not list cannot learn whether the key
// exists. An IAM group member is refused before the evaluation, which its
// groups' policies would join. mark marks each outcome.
func (e *Evaluator) missingObject(ctx context.Context, r *op.Request, a policy.Action, b bucketInputs,
	reqBlock *acl.PublicAccessBlock, key string, ids []*policy.Policy, mark func(error) error,
) error {
	if r.Identity.Admin {
		return e.decided(ctx, a, "missing object, admin", mark(fmt.Errorf("%w: %s", op.ErrNoSuchKey, key)))
	}
	if err := e.deniedAsGroupMember(ctx, &r.Identity); err != nil {
		return e.decided(ctx, a, "missing object, iam group member", mark(err))
	}
	env := baseEnv(e.cfg, r)
	env.Add("s3:prefix", key)
	bid := b.rec.Info.Bucket
	c := check{
		a: policy.S3ListBucket, perm: acl.PermFor(policy.S3ListBucket), arn: policy.BucketARN(bid.Tenant, bid.Name),
		env: env, ids: ids, bucket: b, reqBlock: reqBlock,
	}
	if ok, rule := e.permit(r, &c); !ok {
		return e.decided(ctx, a, "missing object, may not list: "+rule, mark(op.ErrAccessDenied))
	}
	return e.decided(ctx, a, "missing object, may list", mark(fmt.Errorf("%w: %s", op.ErrNoSuchKey, key)))
}

// check is one verify_bucket_permission or verify_object_permission call
// through the request state.
type check struct {
	a    policy.Action
	perm acl.Permission
	arn  policy.ARN
	env  policy.Env
	ids  []*policy.Policy
	// bucket holds the resource; reqBlock is the request's bucket's
	// public-access block, which applies with bucket's own.
	bucket   bucketInputs
	reqBlock *acl.PublicAccessBlock
	// object is the object's ACL for an object check, nil for a bucket check.
	object *acl.Policy
}

// permit is verify_bucket_permission and verify_object_permission through
// the request state (rgw_common.cc:1374-1410 and :1518-1558 at v19.2.6;
// :1396-1440 and :1564-1612 at v20.2.4), and the rule that decided.
// x-amz-expected-bucket-owner must name the request's bucket's owner. An
// account's identity is checked across accounts against the owner of the
// object's ACL, or the bucket's, needing an Allow from its identity policies
// and, separately, an Allow or a grant from the resource; within its account
// a policy must allow, and the ACLs are not consulted.
func (e *Evaluator) permit(r *op.Request, c *check) (ok bool, rule string) {
	id := identity{id: &r.Identity}
	if v, present := header(r.Header, "X-Amz-Expected-Bucket-Owner"); present {
		if r.BucketRec == nil || v != expectedOwner(r.BucketRec.Info.Owner) {
			return false, "expected bucket owner"
		}
	}
	if r.Identity.Account == nil {
		return e.permitOne(r, id, c, false, true, c.bucket.policy, c.ids)
	}
	accountRoot := id.IdentityType() == meta.IdentityRoot
	owner := c.bucket.acl.Owner.ID
	if c.object != nil && c.object.Owner.ID != "" {
		owner = c.object.Owner.ID
	}
	if id.IsOwnerOf(owner) {
		return e.permitOne(r, id, c, accountRoot, false, c.bucket.policy, c.ids)
	}
	if ok, rule = e.permitOne(r, id, c, accountRoot, false, nil, c.ids); !ok {
		return false, "cross-account identity: " + rule
	}
	ok, rule = e.permitOne(r, id, c, false, true, c.bucket.policy, nil)
	return ok, "cross-account resource: " + rule
}

// expectedOwner is to_expected_bucket_owner (rgw_common.cc:232-240 at
// v20.2.4): an account's id, or a user's id without its tenant.
func expectedOwner(o meta.Owner) string {
	if o.User != nil {
		return o.User.ID
	}
	return o.Account
}

// permitOne is the inner verify_bucket_permission or
// verify_object_permission (rgw_common.cc:1342-1372 and :1490-1516 at
// v19.2.6, :1355-1394 and :1528-1562 at v20.2.4): requester pays,
// RestrictPublicBuckets when a resource policy is given, the policies, and
// when they pass and withACL, the ACLs.
func (e *Evaluator) permitOne(r *op.Request, id identity, c *check, accountRoot, withACL bool,
	resource *policy.Policy, ids []*policy.Policy,
) (ok bool, rule string) {
	if !requesterPays(r, id) {
		return false, "requester pays"
	}
	if resource != nil && restricted(r, id, c, resource) {
		return false, "restrict public buckets"
	}
	var effect policy.Effect
	switch effect, rule = e.evaluate(c.env, id, accountRoot, c.a, c.arn, resource, ids); effect {
	case policy.Deny:
		return false, rule
	case policy.Allow:
		return true, rule
	}
	if !withACL {
		return false, "no policy allows"
	}
	if c.object != nil {
		return e.objectACL(r, id, c)
	}
	return bucketACL(r, id, c)
}

// evaluate is evaluate_iam_policies (rgw_common.cc:1170-1248 at v19.2.6,
// :1183-1261 at v20.2.4) without session policies, and the rule that
// decided. Identity policies are evaluated with no identity, as
// eval_identity_or_session_policies does (:1146-1168, :1159-1181), so their
// Principal elements are ignored.
func (e *Evaluator) evaluate(env policy.Env, id identity, accountRoot bool, a policy.Action, arn policy.ARN,
	resource *policy.Policy, ids []*policy.Policy,
) (effect policy.Effect, rule string) {
	identityRes := policy.Pass
	for _, p := range ids {
		switch p.Eval(env, nil, a, &arn, e.sem) {
		case policy.Deny:
			return policy.Deny, "identity policy deny"
		case policy.Allow:
			identityRes = policy.Allow
		}
	}
	if resource != nil {
		switch resource.Eval(env, id, a, &arn, e.sem) {
		case policy.Deny:
			return policy.Deny, "bucket policy deny"
		case policy.Allow:
			return policy.Allow, "bucket policy allow"
		}
	}
	switch {
	case identityRes == policy.Allow:
		return policy.Allow, "identity policy allow"
	case accountRoot:
		return policy.Allow, "account root"
	}
	return policy.Pass, ""
}

// requesterPays is verify_requester_payer_permission (rgw_common.cc:1322-1340
// at v19.2.6, :1335-1353 at v20.2.4) on the request's bucket: the owner pays,
// an anonymous requester never, and anyone else only by sending
// x-amz-request-payer, as a header or else as a query parameter, equal to
// "requester" in any ASCII case.
func requesterPays(r *op.Request, id identity) bool {
	rec := r.BucketRec
	if rec == nil || !rec.Info.RequesterPays || id.IsOwnerOfOwner(rec.Info.Owner) {
		return true
	}
	if id.IsAnonymous() {
		return false
	}
	v, ok := header(r.Header, "X-Amz-Request-Payer")
	if !ok {
		if !hasQuery(r, "x-amz-request-payer") {
			return false
		}
		v = query(r, "x-amz-request-payer")
	}
	return lowerASCII(v) == "requester"
}

// restricted is RestrictPublicBuckets (rgw_common.cc:1374-1380 and
// :1541-1547 at v20.2.4): a public policy refuses everyone outside the
// bucket owner's account, each block judged against its own bucket's owner.
func restricted(r *op.Request, id identity, c *check, resource *policy.Policy) bool {
	if !resource.IsPublic(restrictSemantics) {
		return false
	}
	blocks := []struct {
		block *acl.PublicAccessBlock
		rec   *op.BucketRecord
	}{{c.reqBlock, r.BucketRec}, {c.bucket.block, c.bucket.rec}}
	for _, b := range blocks {
		if b.block != nil && b.rec != nil && b.block.RestrictPublicBuckets && !id.IsOwnerOfOwner(b.rec.Info.Owner) {
			return true
		}
	}
	return false
}

// ignorePublicACLs is whether either bucket's block sets IgnorePublicAcls.
func (c *check) ignorePublicACLs() bool {
	return c.reqBlock != nil && c.reqBlock.IgnorePublicACLs || c.bucket.block != nil && c.bucket.block.IgnorePublicACLs
}

// referer is the Referer the ACL's referer grants match, HTTP_REFERER, which
// beast sets from the last of a repeated header; "" when absent.
func referer(r *op.Request) string {
	v, _ := header(r.Header, "Referer")
	return v
}

// bucketACL is verify_bucket_permission_no_policy (rgw_common.cc:1412-1432
// at v19.2.6, :1442-1469 at v20.2.4): within the permission mask, a grant of
// the bucket's ACL. S3 has no user ACL, so it never grants.
func bucketACL(r *op.Request, id identity, c *check) (ok bool, rule string) {
	if c.perm&id.PermMask() != c.perm {
		return false, "permission mask"
	}
	if c.bucket.acl.Verify(id, c.perm, c.perm, referer(r), c.ignorePublicACLs()) {
		return true, "bucket acl"
	}
	return false, "bucket acl"
}

// objectACL is verify_object_permission_no_policy (rgw_common.cc:1560-1608
// at v19.2.6, :1614-1671 at v20.2.4): a grant of the object's ACL within the
// permission mask, matched with no referer, or with rgw_enforce_swift_acls
// the Swift container flags of the bucket's ACL, which only a Swift client
// writes. Deferring to the bucket's ACLs is Swift's alone.
func (e *Evaluator) objectACL(r *op.Request, id identity, c *check) (ok bool, rule string) {
	mask := id.PermMask()
	if c.object.Verify(id, mask, c.perm, "", c.ignorePublicACLs()) {
		return true, "object acl"
	}
	if !e.cfg.EnforceSwiftACLs || c.perm&mask != c.perm {
		return false, "object acl"
	}
	var swift acl.Permission
	if c.perm&(acl.PermRead|acl.PermReadACP) != 0 {
		swift |= acl.PermReadObjs
	}
	if c.perm&acl.PermWrite != 0 {
		swift |= acl.PermWriteObjs
	}
	if swift != 0 && c.bucket.acl.Verify(id, swift, swift, referer(r), false) {
		return true, "bucket acl swift flags"
	}
	return false, "object acl"
}
