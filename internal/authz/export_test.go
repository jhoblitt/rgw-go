package authz

import (
	"slices"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// View is the applier view of an identity the evaluator decides with.
type View = identity

// ViewOf returns id's applier view.
func ViewOf(id *op.Identity) View { return identity{id: id} }

// The loaders and the tag helpers, reached directly.
var (
	BucketPolicy     = bucketPolicy
	IdentityPolicies = identityPolicies
	PublicAccess     = publicAccess
	AddTags          = addTags
	TagSet           = tagSet
	NeedsTags        = needsTags
	BaseEnv          = baseEnv
)

// EnvPairs returns the keys BuildEnv adds, as key and value, in the order
// radosgw emplaces them, then the write's ACL and tag keys and the
// object-lock keys, which radosgw does not add, and whether the op for a adds
// tags before its own keys.
func EnvPairs(cfg Config, r *op.Request, a policy.Action) (pairs [][2]string, tagsFirst bool) {
	for _, p := range baseKeys(cfg, r, cfg.now().UTC()) {
		pairs = append(pairs, [2]string{p.key, p.value})
	}
	keys, tagsFirst := opKeys(cfg, r, a)
	for _, p := range slices.Concat(keys, writeKeys(r, a), lockKeys(cfg, r, a)) {
		pairs = append(pairs, [2]string{p.key, p.value})
	}
	return pairs, tagsFirst
}

// AmazonPairs returns the keys and values BuildEnv sets for a Deny alone, as
// Amazon S3's request context holds them, in the order it sets them.
func AmazonPairs(cfg Config, r *op.Request, a policy.Action) [][2]string {
	return amazonPairs(cfg, r, a, requestVersion(r))
}

// AmazonPairsOnKey is AmazonPairs for a check on key, an object the request
// names other than its own, as a copy's source or a DeleteObjects key.
func AmazonPairsOnKey(cfg Config, r *op.Request, a policy.Action, key meta.ObjKey) [][2]string {
	return amazonPairs(cfg, r, a, keyVersion(key))
}

// AmazonPairsOnState is AmazonPairs for a check on st, a source object's
// state.
func AmazonPairsOnState(cfg Config, r *op.Request, a policy.Action, st *op.ObjectState) [][2]string {
	return amazonPairs(cfg, r, a, stateVersion(st))
}

func amazonPairs(cfg Config, r *op.Request, a policy.Action, v objectVersion) [][2]string {
	var pairs [][2]string
	ps, _ := amazonKeys(cfg, r, a, v, cfg.now().UTC())
	for _, p := range ps {
		pairs = append(pairs, [2]string{p.key, p.value})
	}
	return pairs
}

// AmazonOther is a second reading BuildEnv records for a Deny: Key holding
// Value alone, or absent when Absent is set.
type AmazonOther struct {
	Key, Value string
	Absent     bool
}

// AmazonOthers returns the second readings BuildEnv records for a Deny, in
// the order it records them.
func AmazonOthers(cfg Config, r *op.Request, a policy.Action) []AmazonOther {
	others := []AmazonOther{}
	_, rs := amazonKeys(cfg, r, a, requestVersion(r), cfg.now().UTC())
	for _, o := range rs {
		others = append(others, AmazonOther{Key: o.key, Value: o.value, Absent: o.absent})
	}
	return others
}

// BuildEnvWithTags is BuildEnv with addTags run where the op for a adds its
// tag keys.
func BuildEnvWithTags(cfg Config, r *op.Request, a policy.Action, addTags func(*policy.Env)) policy.Env {
	return buildEnv(cfg, r, a, addTags, requestVersion(r))
}
