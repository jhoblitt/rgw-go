// Package policy holds radosgw's IAM policy language as rgw-go reproduces it:
// the actions a policy can name and the releases that know them, radosgw's
// wildcard matching of action names and ARNs, ARNs, principals, a request's
// condition environment, and the rules on which Squid and Tentacle differ.
// A Policy holds Statements, and evaluates a request against them as radosgw
// does: any Deny wins, then any Allow, else the policy passes. It also answers
// whether the policy is public.
package policy
