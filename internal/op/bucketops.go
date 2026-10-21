package op

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The bucket ops of this file are rgw_op.cc's RGWCreateBucket,
// RGWDeleteBucket, RGWStatBucket and RGWGetBucketLocation. Where a
// link cites v19.2.6 alone, the v20.2.4 code is the same unless noted.

// loadBucket is rgw_build_bucket_policies' load of the bucket the request
// names, which every S3 op but CreateBucket makes before anything else
// once the requester is authenticated: a missing bucket is NoSuchBucket
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L523-L541;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L553-L571),
// and a bucket of another zonegroup is redirectForeignBucket's
// PermanentRedirect, with r.BucketRec set either way, as s->bucket is, for
// the headers and the usage log.
func loadBucket(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	return redirectForeignBucket(ctx, r)
}

// redirectForeignBucket is rgw_build_bucket_policies' zonegroup check
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L558-L574;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L588-L604):
// a bucket whose zonegroup RGWZoneGroup::equals does not take for this
// one is PermanentRedirect, except for a system request to the master
// zonegroup and for a PUT of an object whose copy source's bucket this
// zonegroup holds (localSource). GetBucketLocation, the third exception,
// loads its bucket without the check.
func redirectForeignBucket(ctx context.Context, r *Request) error {
	zg := r.Env.Zone.ZoneGroup()
	switch {
	case zg.Equals(r.BucketRec.Info.Zonegroup):
		return nil
	case zg.IsMaster && r.Identity.System:
		return nil
	case r.Method == http.MethodPut && r.Object.Name != "" && localSource(ctx, r, zg):
		return nil
	}
	return ErrPermanentRedirect
}

// localSource is s->local_source
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L509-L518;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L539-L548):
// the request names a copy source whose bucket loads and is of zonegroup
// zg. A source that fails to load is no local source, whatever the failure.
func localSource(ctx context.Context, r *Request, zg meta.ZoneGroup) bool {
	if r.SrcBucket == "" {
		return false
	}
	src, err := r.Env.Buckets.GetBucket(ctx, r.SrcTenant, r.SrcBucket)
	return err == nil && zg.Equals(src.Info.Zonegroup)
}

// CreateBucket is RGWCreateBucket
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3170-L3244,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3416-L3707;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3398-L3472,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3644-L3880)
// for S3. Params, when set, is the protocol's get_params, which Execute runs
// first, as radosgw's execute does: it checks the bucket name, builds ACL
// from the request's ACL headers, reads the request's
// CreateBucketConfiguration into LocationConstraint and Placement and the
// object-lock header into ObjectLock. Execute then fills Existed and Result.
type CreateBucket struct {
	Params func(ctx context.Context, o *CreateBucket) error

	// LocationConstraint is the configuration's, without the placement a
	// "zonegroup:placement" constraint names, "" when absent.
	LocationConstraint string
	// Placement is the placement name such a constraint names and the
	// x-amz-storage-class header's class.
	Placement meta.PlacementRule
	// ObjectLock is x-amz-bucket-object-lock-enabled as sent; HasObjectLock
	// reports that it was.
	ObjectLock    string
	HasObjectLock bool
	// BucketIndex reports a valid BucketIndex element in Tentacle's
	// CreateBucketConfiguration, the index radosgw would give the bucket.
	BucketIndex bool
	// ACL is the bucket's policy, whose owner the bucket gets.
	ACL acl.Policy
	// NonPrivateACL reports that the request names an ACL other than
	// private: an x-amz-acl other than private, or a grant header.
	NonPrivateACL bool

	// Existed is set when the bucket already belonged to the ACL's owner,
	// which S3 answers as created.
	Existed bool
	Result  *BucketRecord
}

var _ Op = (*CreateBucket)(nil)

// Name is RGWCreateBucket::name().
func (*CreateBucket) Name() string { return "create_bucket" }

// Action is s3:CreateBucket.
func (*CreateBucket) Action() policy.Action { return policy.S3CreateBucket }

// OpMask is RGW_OP_TYPE_WRITE.
func (*CreateBucket) OpMask() uint32 { return OpTypeWrite }

// Init loads nothing: radosgw skips the bucket load for a create
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1883-L1891;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1886-L1894).
func (*CreateBucket) Init(context.Context, *Request) error { return nil }

// VerifyPermission is RGWCreateBucket::verify_permission: an anonymous
// identity is refused; s3:CreateBucket is authorized against the identity's
// own policies; a bucket outside the identity's tenant is refused; then the
// owner's bucket limit is checked. An identity cannot be a role in phase 1,
// so the cross-tenant exception for roles never applies.
func (o *CreateBucket) VerifyPermission(ctx context.Context, r *Request) error {
	if r.Identity.Anonymous {
		return ErrAccessDenied
	}
	if err := VerifyUserPermission(ctx, r, o.Action()); err != nil {
		return err
	}
	if r.Identity.Tenant != r.Tenant {
		return ErrAccessDenied
	}
	return checkOwnerMaxBuckets(ctx, r)
}

// checkOwnerMaxBuckets is check_owner_max_buckets
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3170-L3213;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3398-L3441):
// the limit is the account's max_buckets for an account's identity and the
// user's otherwise. A negative limit refuses every bucket and zero none;
// otherwise the owner's buckets are counted, in pages of
// rgw_list_buckets_max_chunk or of what the limit leaves when that is more,
// until the limit is reached, TooManyBuckets, or the list ends.
func checkOwnerMaxBuckets(ctx context.Context, r *Request) error {
	owner := r.Identity.Owner
	var remaining int64
	if owner.User == nil {
		acct := r.Identity.Account
		if acct == nil || acct.ID != owner.Account {
			rec, err := r.Env.Accounts.GetAccount(ctx, owner.Account)
			if err != nil {
				return err
			}
			acct = &rec.Info
		}
		remaining = int64(acct.MaxBuckets)
	} else if u := r.Identity.User; u != nil {
		remaining = int64(u.MaxBuckets)
	}
	switch {
	case remaining < 0:
		return ErrAccessDenied
	case remaining == 0:
		return nil
	}
	chunk := DefaultListBucketsChunk
	if r.Env.Conf != nil {
		if v, err := r.Env.Conf.Int64("rgw_list_buckets_max_chunk"); err == nil {
			chunk = ListBucketsChunk(v)
		}
	}
	marker := ""
	for {
		ents, next, more, err := r.Env.Users.ListUserBuckets(ctx, owner, marker, max(chunk, int(remaining)))
		if err != nil {
			return FromRADOS(err, ScopeService)
		}
		remaining -= int64(len(ents))
		if remaining <= 0 {
			return ErrTooManyBuckets
		}
		if !more || len(ents) == 0 {
			return nil
		}
		marker = next
	}
}

// Execute is RGWCreateBucket::execute
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3458-L3707;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3686-L3880)
// for a single-site S3 request. After Params, the object-lock header is
// checked as get_params checks it, true or false in any case and any other
// value InvalidArgument
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2542-L2548 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2695-L2701 at v20.2.4).
// As Amazon S3 requires and radosgw does not (verifyCreateActions), an ACL
// other than private is authorized as s3:PutBucketAcl on the new bucket,
// before the header is checked, and a bucket with object lock then as
// s3:PutBucketObjectLockConfiguration and s3:PutBucketVersioning.
// A requested index is NotImplemented, for a bucket that does not exist,
// after every check radosgw makes before it creates one
// (docs/exclusions.md). The location constraint picks the bucket's
// zonegroup, the placement is selected, and a bucket of the name that exists
// must match the request's zonegroup, placement and ACL. The bucket is then
// created for the ACL's owner with the ACL as its user.rgw.acl, and with
// object lock when the header is true. A bucket that existed and is the
// owner's is Existed, whatever object lock it has; one that is another
// owner's is BucketAlreadyExists, which radosgw answers as created when that
// owner's create lands during this one (docs/exclusions.md).
//
// r.BucketRec carries the usage log's owner and payer, radosgw's
// s->bucket_owner and s->bucket: none before the existing bucket is read,
// then the existing bucket's requester-pays with no owner, and from the
// create on the ACL's owner
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3541-L3581;
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L203-L220).
func (o *CreateBucket) Execute(ctx context.Context, r *Request) error {
	if o.Params != nil {
		if err := o.Params(ctx, o); err != nil {
			return err
		}
	}
	if o.NonPrivateACL {
		if err := verifyCreateActions(ctx, r, policy.S3PutBucketAcl); err != nil {
			return err
		}
	}
	lock := false
	if o.HasObjectLock {
		switch {
		case strings.EqualFold(o.ObjectLock, "true"):
			lock = true
		case !strings.EqualFold(o.ObjectLock, "false"):
			return ErrInvalidArgument
		}
	}
	if lock {
		if err := verifyCreateActions(ctx, r, policy.S3PutBucketObjectLockConfiguration, policy.S3PutBucketVersioning); err != nil {
			return err
		}
	}
	zi := r.Env.Zone
	mine := zi.ZoneGroup()
	bucketZG, err := o.bucketZoneGroup(r, mine)
	if err != nil {
		return err
	}
	rule, err := selectBucketPlacement(bucketZG, r.Identity.User, o.Placement)
	if err != nil {
		return err
	}
	if bucketZG.ID == mine.ID {
		if _, perr := zi.Placement(rule); perr != nil {
			return ErrInvalidLocationConstraint
		}
	}
	existing, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	switch {
	case errors.Is(err, ErrNoSuchBucket):
	case err != nil:
		return err
	default:
		r.BucketRec = &BucketRecord{Info: meta.BucketInfo{RequesterPays: existing.Info.RequesterPays}}
		if cerr := o.checkExisting(ctx, r, existing, bucketZG.ID, rule); cerr != nil {
			return cerr
		}
	}
	// A bucket that exists is answered as radosgw answers it whatever index
	// the request asks for
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L183-L202 at v20.2.4).
	if o.BucketIndex && existing == nil {
		return ErrNotImplemented
	}
	owner := meta.ParseOwner(o.ACL.Owner.ID)
	e := denc.NewEncoder()
	o.ACL.Encode(e, zi.Release())
	r.BucketRec = &BucketRecord{Info: meta.BucketInfo{Owner: owner}}
	rec, err := r.Env.Buckets.CreateBucket(ctx, CreateBucketParams{
		Tenant: r.Tenant, Name: r.Bucket, Owner: owner, Zonegroup: bucketZG.ID, Placement: rule,
		Attrs: map[string][]byte{meta.AttrACL: e.Bytes()}, ObjectLock: lock, Exclusive: true,
	})
	if rec != nil {
		r.BucketRec.Info.RequesterPays = rec.Info.RequesterPays
	}
	switch {
	case errors.Is(err, ErrBucketAlreadyExists) && rec != nil && rec.Info.Owner.String() == owner.String():
		o.Existed, o.Result = true, rec
		return nil
	case err != nil:
		return err
	}
	o.Result = rec
	return nil
}

// verifyCreateActions authorizes each of actions, which Amazon S3 requires
// beside s3:CreateBucket for what the request asks of the new bucket, as the
// requester's own policies decide s3:CreateBucket: no bucket policy exists
// yet. Amazon S3's CreateBucket reference, retrieved 2026-10-21: "if you
// specify an access control list (ACL) and set it to public-read,
// public-read-write, authenticated-read, or if you explicitly specify any
// other custom ACLs, both s3:CreateBucket and s3:PutBucketAcl permissions are
// required", and "if you set x-amz-bucket-object-lock-enabled to true, the
// s3:PutBucketObjectLockConfiguration and s3:PutBucketVersioning permissions
// are required". radosgw's verify_permission checks s3:CreateBucket alone
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3215-L3244 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3443-L3472 at v20.2.4;
// docs/exclusions.md, "CreateBucket with an ACL other than private is
// authorized as s3:PutBucketAcl too" and "CreateBucket with object lock is
// authorized as s3:PutBucketObjectLockConfiguration and s3:PutBucketVersioning
// too"). An admin is let through a denial as Run lets one through the
// permission check.
func verifyCreateActions(ctx context.Context, r *Request, actions ...policy.Action) error {
	for _, a := range actions {
		err := VerifyUserPermission(ctx, r, a)
		if err != nil && (IsBeforeVerify(err) || !r.Identity.Admin || !isAccessDenial(err)) {
			return err
		}
	}
	return nil
}

// bucketZoneGroup is the location-constraint check
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3470-L3522):
// with a period, a constraint names a zonegroup of the period map by its
// api name; without one it must be this zonegroup's api name; neither is
// checked under rgw_relaxed_region_enforcement. A user's bucket must land in
// this zonegroup. A system request's rgwx-zonegroup, which only multisite
// forwarding sends, is not read (docs/exclusions.md, "Multisite").
func (o *CreateBucket) bucketZoneGroup(r *Request, mine meta.ZoneGroup) (meta.ZoneGroup, error) {
	period := r.Env.Zone.Period()
	zg := mine
	if o.LocationConstraint != "" && !relaxedRegionEnforcement(r) {
		if period.ID != "" {
			found, ok := zoneGroupByAPI(period.PeriodMap.ZoneGroups, o.LocationConstraint)
			if !ok {
				return meta.ZoneGroup{}, ErrInvalidLocationConstraint.WithMessage(
					fmt.Sprintf("The %s location constraint is not valid.", o.LocationConstraint))
			}
			zg = found
		} else if o.LocationConstraint != mine.APIName {
			return meta.ZoneGroup{}, illegalLocation(o.LocationConstraint)
		}
	}
	enforce := period.ID == "" || !r.Identity.System || !mine.IsMaster
	if enforce && zg.ID != mine.ID {
		return meta.ZoneGroup{}, illegalLocation(zg.APIName)
	}
	return zg, nil
}

// illegalLocation is ERR_ILLEGAL_LOCATION_CONSTRAINT_EXCEPTION with
// radosgw's message for the constraint name.
func illegalLocation(name string) error {
	return ErrIllegalLocationConstraint.WithMessage(fmt.Sprintf(
		"The %s location constraint is incompatible for the region specific endpoint this request was sent to.", name))
}

// relaxedRegionEnforcement is rgw_relaxed_region_enforcement, false when it
// cannot be read.
func relaxedRegionEnforcement(r *Request) bool {
	if r.Env.Conf == nil {
		return false
	}
	v, err := r.Env.Conf.Bool("rgw_relaxed_region_enforcement")
	return err == nil && v
}

// zoneGroupByAPI is RGWPeriodMap's zonegroups_by_api lookup: the map is
// built over the zonegroups in id order, so of two with one api name the
// later id wins
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L1039-L1047).
func zoneGroupByAPI(zgs map[string]meta.ZoneGroup, api string) (meta.ZoneGroup, bool) {
	var found meta.ZoneGroup
	ok := false
	for _, id := range slices.Sorted(maps.Keys(zgs)) {
		if zgs[id].APIName == api {
			found, ok = zgs[id], true
		}
	}
	return found, ok
}

// selectBucketPlacement is select_bucket_placement
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3416-L3456;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3644-L3684):
// a rule without a name inherits the user's default placement and then the
// zonegroup's, and still without one the zonegroup is misconfigured; the
// rule must name a placement target of the zonegroup whose tags, when it
// has any, include one of the user's.
func selectBucketPlacement(zg meta.ZoneGroup, user *meta.UserInfo, rule meta.PlacementRule) (meta.PlacementRule, error) {
	if rule.Name == "" {
		if user != nil {
			rule = rule.InheritFrom(user.DefaultPlacement)
		}
		if rule.Name == "" {
			rule = rule.InheritFrom(zg.DefaultPlacement)
			if rule.Name == "" {
				return rule, ErrZonegroupPlacementMisconfig
			}
		}
	}
	target, ok := zg.PlacementTargets[rule.Name]
	if !ok {
		return rule, ErrInvalidLocationConstraint
	}
	if len(target.Tags) > 0 && (user == nil || !slices.ContainsFunc(user.PlacementTags, func(t string) bool { return slices.Contains(target.Tags, t) })) {
		return rule, ErrAccessDenied
	}
	return rule, nil
}

// checkExisting is the existing-bucket checks
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3549-L3579;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3777-L3807):
// a request may not change the bucket's zonegroup, unless it is a system
// request, its placement, or its ACL, each refusal BucketAlreadyExists with
// radosgw's message. An ACL that does not decode is not compared.
func (o *CreateBucket) checkExisting(ctx context.Context, r *Request, existing *BucketRecord, zonegroup string, rule meta.PlacementRule) error {
	info := &existing.Info
	if !r.Identity.System && zonegroup != info.Zonegroup {
		return ErrBucketAlreadyExists.WithMessage("Cannot modify existing bucket's zonegroup")
	}
	if rule.Name != info.PlacementRule.Name || rule.CanonicalStorageClass() != info.PlacementRule.CanonicalStorageClass() {
		return ErrBucketAlreadyExists.WithMessage("Cannot modify existing bucket's placement rule")
	}
	if old, err := BucketACLFor(ctx, existing); err == nil && !samePolicy(old, o.ACL) {
		return ErrBucketAlreadyExists.WithMessage("Cannot modify existing access control policy")
	}
	return nil
}

// samePolicy is RGWAccessControlPolicy's operator==
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L22-L69):
// the owners, the grant multimaps in order, the user and group maps and the
// referer lists all equal, which the two encodings being equal decides.
func samePolicy(a, b acl.Policy) bool {
	ea, eb := denc.NewEncoder(), denc.NewEncoder()
	a.Encode(ea, denc.Squid)
	b.Encode(eb, denc.Squid)
	return bytes.Equal(ea.Bytes(), eb.Bytes())
}

// Complete does nothing: the handler logs the request's usage once its
// response is written.
func (*CreateBucket) Complete(context.Context, *Request) {}

// DeleteBucket is RGWDeleteBucket
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3709-L3804;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3938-L4013)
// for a single-site request.
type DeleteBucket struct{}

var _ Op = (*DeleteBucket)(nil)

// Name is RGWDeleteBucket::name().
func (*DeleteBucket) Name() string { return "delete_bucket" }

// Action is s3:DeleteBucket.
func (*DeleteBucket) Action() policy.Action { return policy.S3DeleteBucket }

// OpMask is RGW_OP_TYPE_DELETE.
func (*DeleteBucket) OpMask() uint32 { return OpTypeDelete }

// Init loads the bucket.
func (*DeleteBucket) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWDeleteBucket::verify_permission: s3:DeleteBucket
// against the bucket.
func (o *DeleteBucket) VerifyPermission(ctx context.Context, r *Request) error {
	a := o.Action()
	return VerifyBucketPermission(ctx, r, a, acl.PermFor(a))
}

// Execute removes the bucket. A plain ConcurrentModification from the
// store is success, as radosgw makes it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3792-L3797;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4001-L4006);
// ErrRemovalRaced, a removal that lost the entry point after claiming the
// bucket, which stays, and ErrRenamePending are the 409s they are. A
// bucket an unfinished admin rename marks is refused with
// ConcurrentModification first, as its other name shares the index
// (docs/exclusions.md, "Bucket link and unlink write differently from
// radosgw").
func (*DeleteBucket) Execute(ctx context.Context, r *Request) error {
	if err := refuseUnfinishedRename(ctx, r.Env, r.BucketRec); err != nil {
		return err
	}
	err := r.Env.Buckets.DeleteBucket(ctx, r.BucketRec)
	if errors.Is(err, ErrConcurrentModification) && !errors.Is(err, ErrRenamePending) && !errors.Is(err, ErrRemovalRaced) {
		return nil
	}
	return err
}

// Complete does nothing: the handler logs the request's usage once its
// response is written.
func (*DeleteBucket) Complete(context.Context, *Request) {}

// StatBucket is RGWStatBucket
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2983-L3031;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3215-L3265),
// the bucket HEAD.
type StatBucket struct {
	// ReadStats asks for the bucket's stats: always on Squid, and on
	// Tentacle only with read-stats.
	ReadStats bool
	// Stats is the Main category's, when read.
	Stats Stats
}

var _ Op = (*StatBucket)(nil)

// Name is RGWStatBucket::name().
func (*StatBucket) Name() string { return "stat_bucket" }

// Action is s3:ListBucket, which governs a bucket HEAD
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2989-L2991).
func (*StatBucket) Action() policy.Action { return policy.S3ListBucket }

// OpMask is RGW_OP_TYPE_READ.
func (*StatBucket) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*StatBucket) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission authorizes s3:ListBucket against the bucket.
func (o *StatBucket) VerifyPermission(ctx context.Context, r *Request) error {
	a := o.Action()
	return VerifyBucketPermission(ctx, r, a, acl.PermFor(a))
}

// Execute reads the stats when asked, load_bucket_stats' Main category.
// Squid's radosgw reads the bucket once more first
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3025-L3028),
// which only a delete in between changes.
func (o *StatBucket) Execute(ctx context.Context, r *Request) error {
	if !o.ReadStats {
		return nil
	}
	st, err := r.Env.Stats.BucketStats(ctx, r.BucketRec)
	if err != nil {
		return err
	}
	o.Stats = st
	return nil
}

// Complete does nothing: the handler logs the request's usage once its
// response is written.
func (*StatBucket) Complete(context.Context, *Request) {}

// GetBucketLocation is RGWGetBucketLocation
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3136-L3147;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3364-L3375)
// with the api name RGWGetBucketLocation_ObjStore_S3 renders
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2129-L2150;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2230-L2253).
type GetBucketLocation struct {
	APIName string
}

var _ Op = (*GetBucketLocation)(nil)

// Name is RGWGetBucketLocation::name().
func (*GetBucketLocation) Name() string { return "get_bucket_location" }

// Action is s3:GetBucketLocation.
func (*GetBucketLocation) Action() policy.Action { return policy.S3GetBucketLocation }

// OpMask is RGW_OP_TYPE_READ.
func (*GetBucketLocation) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket without redirecting one of another zonegroup, the
// exception rgw_build_bucket_policies makes for this op
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L567;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L597).
func (*GetBucketLocation) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	return nil
}

// VerifyPermission authorizes s3:GetBucketLocation against the bucket.
func (o *GetBucketLocation) VerifyPermission(ctx context.Context, r *Request) error {
	a := o.Action()
	return VerifyBucketPermission(ctx, r, a, acl.PermFor(a))
}

// Execute names the bucket's zonegroup as RGWSI_Zone::get_zonegroup finds
// it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_zone.cc#L634-L643;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_zone.cc#L438-L447):
// this zonegroup by its api name; with a period, the period map's by its
// api name, the one named "default" for an empty id, and a zonegroup the
// map lacks by its id unless that is "default"; without a period, any other
// zonegroup is the empty one get_zonegroup leaves unfilled, which has no
// api name.
func (o *GetBucketLocation) Execute(_ context.Context, r *Request) error {
	id := r.BucketRec.Info.Zonegroup
	zi := r.Env.Zone
	if mine := zi.ZoneGroup(); id == mine.ID {
		o.APIName = mine.APIName
		return nil
	}
	period := zi.Period()
	if period.ID == "" {
		return nil
	}
	key := id
	if key == "" {
		key = "default"
	}
	if zg, ok := period.PeriodMap.ZoneGroups[key]; ok {
		o.APIName = zg.APIName
		return nil
	}
	if id != "default" {
		o.APIName = id
	}
	return nil
}

// Complete does nothing: the handler logs the request's usage once its
// response is written.
func (*GetBucketLocation) Complete(context.Context, *Request) {}
