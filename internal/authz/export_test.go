package authz

import (
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
	NeedsTags        = needsTags
	BaseEnv          = baseEnv
)

// EnvPairs returns the keys BuildEnv adds, as key and value, in the order
// radosgw emplaces them, and whether the op for a adds tags before its own
// keys.
func EnvPairs(cfg Config, r *op.Request, a policy.Action) (pairs [][2]string, tagsFirst bool) {
	for _, p := range baseKeys(cfg, r) {
		pairs = append(pairs, [2]string{p.key, p.value})
	}
	keys, tagsFirst := opKeys(cfg, r, a)
	for _, p := range keys {
		pairs = append(pairs, [2]string{p.key, p.value})
	}
	return pairs, tagsFirst
}

// BuildEnvWithTags is BuildEnv with addTags run where the op for a adds its
// tag keys.
func BuildEnvWithTags(cfg Config, r *op.Request, a policy.Action, addTags func(*policy.Env)) policy.Env {
	return buildEnv(cfg, r, a, addTags)
}
