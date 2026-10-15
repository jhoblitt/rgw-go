package s3

import (
	"slices"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// routeSet is one feature's routes in one scope, each bound to that scope.
type routeSet struct {
	scope    op.Scope
	handlers map[string]HandlerFunc
}

// featureRoutes is every feature's route sets. Each feature's function sits
// in that feature's own file, so a feature binds its routes there without
// editing the handler or another feature's file.
func featureRoutes(env *op.Env, auth Authenticator) []routeSet {
	return slices.Concat(
		versioningRoutes(env),
		objectLockRoutes(env),
		lifecycleRoutes(env),
		layoutRoutes(env),
		corsRoutes(env),
		websiteRoutes(env),
		publicAccessRoutes(env),
		encryptionRoutes(env),
		policyStatusRoutes(env),
		ownershipRoutes(env),
		postObjectRoutes(env, auth),
	)
}
