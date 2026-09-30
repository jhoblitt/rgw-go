// Package policy holds radosgw's IAM policy vocabulary. Action is every action
// a policy can name, numbered as rgw::IAM::action_t numbers them: the s3
// actions every op names for its permission check, then the s3-object-lambda,
// iam, sts, sns and organizations ones. Known says which of them a release
// accepts and ActionSet holds a statement's actions; MatchAction, MatchPolicy
// and MatchWildcards match action names and ARNs against a policy's patterns.
package policy
