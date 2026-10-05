package authz

import (
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// The attrs the loaders read, from rgw_common.h (:154-157 at v19.2.6,
// :173-176 at v20.2.4). The ACLs are op.BucketACLFor's and op.ObjectACLFor's,
// and the tags tags.Attr.
const (
	attrIAMPolicy     = meta.AttrPrefix + "iam-policy"
	attrUserPolicy    = meta.AttrPrefix + "user-policy"
	attrManagedPolicy = meta.AttrPrefix + "managed-policy"
	attrPublicAccess  = meta.AttrPrefix + "public-access"
)

// bucketPolicy is get_iam_policy_from_attr (rgw_op.cc:329-338 at v19.2.6,
// rgw_common.cc:3276-3285 at v20.2.4): the bucket's stored policy parsed with
// tenant and, as for every stored policy, unsupported principals dropped; nil
// for a bucket without one. A policy that does not parse is the
// *policy.ParseError, wrapped.
func bucketPolicy(rec *op.BucketRecord, tenant string, r denc.Release) (*policy.Policy, error) {
	if rec == nil {
		return nil, nil
	}
	b, ok := rec.Attrs[attrIAMPolicy]
	if !ok {
		return nil, nil
	}
	p, err := policy.Parse(string(b), policy.ParseOptions{Tenant: &tenant, Release: r})
	if err != nil {
		return nil, fmt.Errorf("parsing the policy of bucket %s: %w", rec.Info.Bucket.Name, err)
	}
	return p, nil
}

// identityPolicies are the identity's own policies as
// load_account_and_policies loads them (rgw_auth.cc:135-187 at v19.2.6 and
// v20.2.4): its inline policies, parsed with PolicyTenant, then the managed
// policies it attaches. The policies of the IAM groups it belongs to are not
// among them. An attr that does not decode, or a policy that does not parse,
// is an error, where radosgw fails the authentication.
func identityPolicies(id *op.Identity, r denc.Release) ([]*policy.Policy, error) {
	var ps []*policy.Policy
	if b, ok := id.Attrs[attrUserPolicy]; ok {
		inline, err := policy.DecodeUserPolicies(b, PolicyTenant(id), r)
		if err != nil {
			return nil, fmt.Errorf("loading identity policies: %w", err)
		}
		ps = append(ps, inline...)
	}
	if b, ok := id.Attrs[attrManagedPolicy]; ok {
		managed, err := policy.DecodeManagedPolicies(b, r)
		if err != nil {
			return nil, fmt.Errorf("loading identity policies: %w", err)
		}
		ps = append(ps, managed...)
	}
	return ps, nil
}

// publicAccess is get_public_access_conf_from_attr (rgw_op.cc:340-355 at
// v19.2.6, :370-385 at v20.2.4): the bucket's public-access block, nil when
// absent. radosgw takes a block that does not decode for none, which turns
// off IgnorePublicAcls, BlockPublicPolicy and RestrictPublicBuckets; rgw-go
// refuses the request instead with AccessDenied, as radosgw refuses one whose
// stored bucket policy does not parse (rgw_op.cc:598-615 at v19.2.6, :628-645
// at v20.2.4; docs/exclusions.md).
func publicAccess(rec *op.BucketRecord) (*acl.PublicAccessBlock, error) {
	if rec == nil {
		return nil, nil
	}
	b, ok := rec.Attrs[attrPublicAccess]
	if !ok {
		return nil, nil
	}
	d := denc.NewDecoder(b)
	pab := acl.DecodePublicAccessBlock(d)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("%w: decoding the public access block of bucket %s: %w",
			op.ErrAccessDenied, rec.Info.Bucket.Name, err)
	}
	return &pab, nil
}

// The condition key prefixes rgw_check_policy_condition looks for
// (rgw_op.cc:109-111 at v19.2.6, :120-122 at v20.2.4).
const (
	existingObjectTagPrefix = "s3:ExistingObjectTag"
	resourceTagPrefix       = "s3:ResourceTag"
	runtimeResourceTag      = "${s3:ResourceTag"
)

// needsTags is rgw_check_policy_condition (rgw_op.cc:776-821 at v19.2.6,
// :812-857 at v20.2.4) over policies, nil ones skipped: existing when a
// condition key starts with s3:ExistingObjectTag, which a caller that does not
// check existing tags ignores, and resource when one starts with
// s3:ResourceTag or a condition value with ${s3:ResourceTag, each prefix
// matched without regard to ASCII case.
func needsTags(policies ...*policy.Policy) (existing, resource bool) {
	for _, p := range policies {
		if p == nil {
			continue
		}
		existing = existing || p.HasConditionKeyPrefix(existingObjectTagPrefix)
		resource = resource || p.HasConditionKeyPrefix(resourceTagPrefix) ||
			p.HasConditionValuePrefix(runtimeResourceTag)
	}
	return existing, resource
}

// addTags is rgw_iam_add_tags_from_bl (rgw_op.cc:708-725 at v19.2.6,
// :738-755 at v20.2.4): each tag of attrs' tag set, in its order, under
// s3:ExistingObjectTag/<key> when existing and then s3:ResourceTag/<key> when
// resource. A tag set that does not decode adds nothing; radosgw returns -EIO
// before adding a key, and every caller ignores it.
func addTags(env *policy.Env, attrs map[string][]byte, existing, resource bool) {
	b, ok := attrs[tags.Attr]
	if !ok || !existing && !resource {
		return
	}
	d := denc.NewDecoder(b)
	set := tags.Decode(d)
	if d.Err() != nil {
		return
	}
	for _, t := range set.Tags {
		if existing {
			env.Add(existingObjectTagPrefix+"/"+t.Key, t.Value)
		}
		if resource {
			env.Add(resourceTagPrefix+"/"+t.Key, t.Value)
		}
	}
}
