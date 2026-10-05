// Package authz is radosgw's authorization of a request: the identity as
// its rgw::auth::LocalApplier sees it, the IAM condition environment
// (rgw_build_iam_environment and the keys each op adds before
// verify_permission), the loaders of the ACLs, bucket policies, identity
// policies and public-access blocks stored on buckets, objects and users,
// and the tag keys a policy's conditions ask for. The rules are radosgw's at
// v19.2.6 and v20.2.4; where the two differ, Config.Release selects the
// cluster's.
package authz
