package acl_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// fakeIdentity answers acl.Identity as rgw::auth::LocalApplier does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1032-L1056 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth.cc#L1052-L1076 at v20.2.4):
// a user, matched by its id, and the account it belongs to when account is
// set. Ids are compared in their to_string form.
type fakeIdentity struct {
	uid     string
	account string
}

func (f fakeIdentity) IsOwnerOf(ownerID string) bool {
	return ownerID == f.uid || (f.account != "" && ownerID == f.account)
}

func (f fakeIdentity) PermsFromACLSpec(userMap map[string]int32) acl.Permission {
	perm := userMap[f.uid]
	if f.account != "" {
		perm |= userMap[f.account]
	}
	return acl.Permission(perm)
}

// IsAnonymous is rgw::auth::Identity's, which no applier overrides:
// is_owner_of the anonymous user.
func (f fakeIdentity) IsAnonymous() bool { return f.IsOwnerOf("anonymous") }

// The requesters: alice owns every policy the specs build, bob holds no grant
// of his own, and carol is a user of an account.
var (
	alice     = fakeIdentity{uid: "alice"}
	bob       = fakeIdentity{uid: "bob"}
	anonymous = fakeIdentity{uid: "anonymous"}
	carol     = fakeIdentity{uid: "carol", account: "RGW12345678901234567"}
)

func userGrant(id string, p acl.Permission) acl.Grant {
	return acl.Grant{Type: acl.GranteeCanonUser, ID: id, Permission: p}
}

func emailGrant(addr string, p acl.Permission) acl.Grant {
	return acl.Grant{Type: acl.GranteeEmail, Email: addr, Permission: p}
}

func groupGrant(g uint32, p acl.Permission) acl.Grant {
	return acl.Grant{Type: acl.GranteeGroup, Group: g, Permission: p}
}

func refererGrant(spec string, p acl.Permission) acl.Grant {
	return acl.Grant{Type: acl.GranteeReferer, URLSpec: spec, Permission: p}
}

// alicesPolicy returns a policy alice owns holding grants, added in order.
func alicesPolicy(grants ...acl.Grant) acl.Policy {
	p := acl.Policy{Owner: acl.Owner{ID: "alice", DisplayName: "Alice"}}
	for _, g := range grants {
		p.ACL.AddGrant(g)
	}
	return p
}

// alicesDefault is the policy radosgw gives a bucket alice creates.
func alicesDefault() acl.Policy {
	return acl.DefaultPolicy(meta.UserOwner(meta.ParseUserID("alice")), "Alice")
}

var _ = Describe("PermFor", func() {
	DescribeTable("gives the permission Tentacle's op_to_perm gives the action",
		func(a policy.Action, want acl.Permission) {
			Expect(acl.PermFor(a)).To(Equal(want), "%s", a)
		},
		Entry("an object read", policy.S3GetObject, acl.PermRead),
		Entry("a bucket listing", policy.S3ListBucket, acl.PermRead),
		Entry("an object write", policy.S3PutObject, acl.PermWrite),
		Entry("a bucket delete", policy.S3DeleteBucket, acl.PermWrite),
		Entry("a bucket-configuration read", policy.S3GetBucketAcl, acl.PermReadACP),
		Entry("a bucket-configuration write", policy.S3PutBucketPolicy, acl.PermWriteACP),
		Entry("the s3 wildcard", policy.S3All, acl.PermFullControl),
		Entry("Tentacle's GetObjectAttributes", policy.S3GetObjectAttributes, acl.PermRead),
		Entry("Tentacle's GetObjectVersionAttributes", policy.S3GetObjectVersionAttributes, acl.PermRead),
		Entry("Tentacle's GetObjectVersionForReplication", policy.S3GetObjectVersionForReplication, acl.PermRead),
		Entry("Tentacle's ReplicateDelete", policy.S3ReplicateDelete, acl.PermWrite),
		Entry("Tentacle's ReplicateObject", policy.S3ReplicateObject, acl.PermWrite),
		Entry("Tentacle's ReplicateTags", policy.S3ReplicateTags, acl.PermWrite),
		Entry("Tentacle's PostBucketLogging", policy.S3PostBucketLogging, acl.PermWriteACP),
		Entry("ceph main's PutAccountPublicAccessBlock", policy.S3PutAccountPublicAccessBlock, acl.PermInvalid),
		Entry("ceph main's GetAccountPublicAccessBlock", policy.S3GetAccountPublicAccessBlock, acl.PermInvalid),
		Entry("an s3 action the switch leaves out", policy.S3DescribeJob, acl.PermInvalid),
		Entry("v21.1.2's GetBucketOwnershipControls, rgw_iam_policy.h:314-315 at v21.1.2", policy.S3GetBucketOwnershipControls, acl.PermReadACP),
		Entry("v21.1.2's PutBucketOwnershipControls, rgw_iam_policy.h:338-339 at v21.1.2", policy.S3PutBucketOwnershipControls, acl.PermWriteACP),
		Entry("the first action past the s3 block", policy.S3All+1, acl.PermInvalid),
	)
	It("maps every s3 action as op_to_perm does at v20.2.4, with v21.1.2's two ownership controls rows", func() {
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L247-L334 at v20.2.4,
		// row for row; v19.2.6's table
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L237-L317)
		// is the same less the seven actions only Tentacle knows, and
		// v21.1.2's
		// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_iam_policy.h#L256-L345 at v21.1.2)
		// the same plus the ownership controls rows.
		table := map[acl.Permission][]policy.Action{
			acl.PermRead: {
				policy.S3GetObject,
				policy.S3GetObjectTorrent,
				policy.S3GetObjectVersion,
				policy.S3GetObjectVersionTorrent,
				policy.S3GetObjectTagging,
				policy.S3GetObjectVersionTagging,
				policy.S3GetObjectRetention,
				policy.S3GetObjectLegalHold,
				policy.S3GetObjectAttributes,
				policy.S3GetObjectVersionAttributes,
				policy.S3ListAllMyBuckets,
				policy.S3ListBucket,
				policy.S3ListBucketMultipartUploads,
				policy.S3ListBucketVersions,
				policy.S3ListMultipartUploadParts,
				policy.S3GetObjectVersionForReplication,
			},
			acl.PermWrite: {
				policy.S3AbortMultipartUpload,
				policy.S3CreateBucket,
				policy.S3DeleteBucket,
				policy.S3DeleteObject,
				policy.S3DeleteObjectVersion,
				policy.S3PutObject,
				policy.S3PutObjectTagging,
				policy.S3PutObjectVersionTagging,
				policy.S3DeleteObjectTagging,
				policy.S3DeleteObjectVersionTagging,
				policy.S3RestoreObject,
				policy.S3PutObjectRetention,
				policy.S3PutObjectLegalHold,
				policy.S3BypassGovernanceRetention,
				policy.S3ReplicateDelete,
				policy.S3ReplicateObject,
				policy.S3ReplicateTags,
			},
			acl.PermReadACP: {
				policy.S3GetAccelerateConfiguration,
				policy.S3GetBucketAcl,
				policy.S3GetBucketCORS,
				policy.S3GetBucketEncryption,
				policy.S3GetBucketLocation,
				policy.S3GetBucketLogging,
				policy.S3GetBucketNotification,
				policy.S3GetBucketPolicy,
				policy.S3GetBucketPolicyStatus,
				policy.S3GetBucketRequestPayment,
				policy.S3GetBucketTagging,
				policy.S3GetBucketVersioning,
				policy.S3GetBucketWebsite,
				policy.S3GetLifecycleConfiguration,
				policy.S3GetObjectAcl,
				policy.S3GetObjectVersionAcl,
				policy.S3GetReplicationConfiguration,
				policy.S3GetBucketObjectLockConfiguration,
				policy.S3GetBucketPublicAccessBlock,
				policy.S3GetBucketOwnershipControls,
			},
			acl.PermWriteACP: {
				policy.S3DeleteBucketPolicy,
				policy.S3DeleteBucketWebsite,
				policy.S3DeleteReplicationConfiguration,
				policy.S3PutAccelerateConfiguration,
				policy.S3PutBucketAcl,
				policy.S3PutBucketCORS,
				policy.S3PutBucketEncryption,
				policy.S3PutBucketLogging,
				policy.S3PostBucketLogging,
				policy.S3PutBucketNotification,
				policy.S3PutBucketPolicy,
				policy.S3PutBucketRequestPayment,
				policy.S3PutBucketTagging,
				policy.S3PutBucketVersioning,
				policy.S3PutBucketWebsite,
				policy.S3PutLifecycleConfiguration,
				policy.S3PutObjectAcl,
				policy.S3PutObjectVersionAcl,
				policy.S3PutReplicationConfiguration,
				policy.S3PutBucketObjectLockConfiguration,
				policy.S3PutBucketPublicAccessBlock,
				policy.S3PutBucketOwnershipControls,
			},
			acl.PermFullControl: {policy.S3All},
		}
		want := map[policy.Action]acl.Permission{}
		for perm, actions := range table {
			for _, a := range actions {
				Expect(want).NotTo(HaveKey(a), "%s is listed twice", a)
				want[a] = perm
			}
		}
		for a := policy.S3GetObject; a <= policy.S3All; a++ {
			perm, listed := want[a]
			if !listed {
				perm = acl.PermInvalid
			}
			Expect(acl.PermFor(a)).To(Equal(perm), "%s", a)
		}
	})
})

var _ = Describe("DefaultPolicy", func() {
	DescribeTable("is create_default: the owner, with one FULL_CONTROL grant to itself",
		func(owner meta.Owner, id string) {
			Expect(acl.DefaultPolicy(owner, "Alice")).To(Equal(acl.Policy{
				Owner: acl.Owner{ID: id, DisplayName: "Alice"},
				ACL: acl.List{
					UserMap: map[string]int32{id: 15},
					Grants: []acl.GrantEntry{{Key: id, Grant: acl.Grant{
						Type: acl.GranteeCanonUser, ID: id, Name: "Alice", Permission: acl.PermFullControl,
					}}},
				},
			}))
		},
		Entry("a tenanted user", meta.UserOwner(meta.ParseUserID("t$alice")), "t$alice"),
		Entry("an account", meta.AccountOwner("RGW12345678901234567"), "RGW12345678901234567"),
	)
})

var _ = Describe("List.AddGrant", func() {
	It("keys a canonical user's grants by its id, repeating the key and or-ing the user map", func() {
		var l acl.List
		l.AddGrant(userGrant("t$alice", acl.PermRead))
		l.AddGrant(userGrant("t$alice", acl.PermWrite))
		Expect(l).To(Equal(acl.List{
			UserMap: map[string]int32{"t$alice": 3},
			Grants: []acl.GrantEntry{
				{Key: "t$alice", Grant: userGrant("t$alice", acl.PermRead)},
				{Key: "t$alice", Grant: userGrant("t$alice", acl.PermWrite)},
			},
		}))
	})
	It("keeps a canonical user's id in its to_string form, the only form C++ holds", func() {
		var l acl.List
		l.AddGrant(userGrant("$alice", acl.PermRead))
		Expect(l).To(Equal(acl.List{
			UserMap: map[string]int32{"alice": 1},
			Grants:  []acl.GrantEntry{{Key: "alice", Grant: userGrant("alice", acl.PermRead)}},
		}))
	})
	It("keys an email grant by its address and registers it in the user map", func() {
		var l acl.List
		l.AddGrant(emailGrant("a@example.com", acl.PermReadACP))
		Expect(l).To(Equal(acl.List{
			UserMap: map[string]int32{"a@example.com": 4},
			Grants:  []acl.GrantEntry{{Key: "a@example.com", Grant: emailGrant("a@example.com", acl.PermReadACP)}},
		}))
	})
	It("keys a group grant under the empty key and registers it in the group map", func() {
		var l acl.List
		l.AddGrant(groupGrant(acl.GroupAuthenticatedUsers, acl.PermWrite))
		Expect(l).To(Equal(acl.List{
			GroupMap: map[uint32]int32{acl.GroupAuthenticatedUsers: 2},
			Grants:   []acl.GrantEntry{{Key: "", Grant: groupGrant(acl.GroupAuthenticatedUsers, acl.PermWrite)}},
		}))
	})
	It("keys a referer grant under the empty key and appends it to the referer list", func() {
		var l acl.List
		l.AddGrant(refererGrant(".example.com", acl.PermRead))
		Expect(l).To(Equal(acl.List{
			RefererList: []acl.Referer{{URLSpec: ".example.com", Perm: 1}},
			Grants:      []acl.GrantEntry{{Key: "", Grant: refererGrant(".example.com", acl.PermRead)}},
		}))
	})
	It("registers the wildcard referer as the AllUsers group too", func() {
		var l acl.List
		l.AddGrant(refererGrant("*", acl.PermRead))
		Expect(l.GroupMap).To(Equal(map[uint32]int32{acl.GroupAllUsers: 1}))
		Expect(l.RefererList).To(Equal([]acl.Referer{{URLSpec: "*", Perm: 1}}))
	})
	It("keys an unknown grantee under the empty key and registers nothing", func() {
		var l acl.List
		g := acl.Grant{Type: acl.GranteeUnknown, Permission: acl.PermRead}
		l.AddGrant(g)
		Expect(l).To(Equal(acl.List{Grants: []acl.GrantEntry{{Key: "", Grant: g}}}))
	})
})

var _ = Describe("List.RemoveCanonUserGrant", func() {
	It("drops every grant under the owner's key and its user map entry, and nothing else", func() {
		var l acl.List
		l.AddGrant(userGrant("alice", acl.PermRead))
		l.AddGrant(userGrant("bob", acl.PermRead))
		l.AddGrant(groupGrant(acl.GroupAllUsers, acl.PermRead))
		l.AddGrant(userGrant("alice", acl.PermWriteACP))
		l.RemoveCanonUserGrant("alice")
		Expect(l).To(Equal(acl.List{
			UserMap:  map[string]int32{"bob": 1},
			GroupMap: map[uint32]int32{acl.GroupAllUsers: 1},
			Grants: []acl.GrantEntry{
				{Key: "bob", Grant: userGrant("bob", acl.PermRead)},
				{Key: "", Grant: groupGrant(acl.GroupAllUsers, acl.PermRead)},
			},
		}))
	})
	It("names the owner by its to_string form", func() {
		var l acl.List
		l.AddGrant(userGrant("alice", acl.PermRead))
		l.RemoveCanonUserGrant("$alice")
		Expect(l.Grants).To(BeEmpty())
		Expect(l.UserMap).To(BeEmpty())
	})
})

var _ = Describe("Policy.Perm", func() {
	DescribeTable("is the flags of mask the policy grants",
		func(p acl.Policy, id fakeIdentity, mask acl.Permission, referer string, want acl.Permission) {
			Expect(p.Perm(id, mask, referer, false)).To(Equal(want), "mask %#x, referer %q", mask, referer)
		},
		Entry("a user's grant, cut to the mask",
			alicesPolicy(userGrant("bob", acl.PermFullControl)), bob, acl.PermRead, "", acl.PermRead),
		Entry("the owner's implicit ACL flags, only as far as the mask names them",
			alicesPolicy(), alice, acl.PermRead|acl.PermReadACP, "", acl.PermReadACP),
		Entry("a group's grant, or-ed with the user's",
			alicesPolicy(userGrant("bob", acl.PermRead), groupGrant(acl.GroupAllUsers, acl.PermWrite)),
			bob, acl.PermRead|acl.PermWrite, "", acl.PermRead|acl.PermWrite),
		Entry("a matching referer grant, replacing what the other grants gave",
			alicesPolicy(groupGrant(acl.GroupAllUsers, acl.PermWrite), refererGrant(".example.com", acl.PermRead)),
			bob, acl.PermRead|acl.PermWrite, "http://a.example.com/", acl.PermRead),
		Entry("no referer grant once the other grants cover the mask",
			alicesPolicy(groupGrant(acl.GroupAllUsers, acl.PermRead), refererGrant(".evil.com", 0)),
			bob, acl.PermRead, "http://x.evil.com/", acl.PermRead),
	)
})

var _ = Describe("Policy.Verify", func() {
	DescribeTable("the owner and a stranger",
		func(p acl.Policy, id fakeIdentity, perm acl.Permission, want bool) {
			Expect(p.Verify(id, acl.PermFullControl, perm, "", false)).To(Equal(want), "perm %#x", perm)
		},
		Entry("the owner reads through its FULL_CONTROL grant", alicesDefault(), alice, acl.PermRead, true),
		Entry("the owner writes the ACL through its FULL_CONTROL grant", alicesDefault(), alice, acl.PermWriteACP, true),
		Entry("a stranger cannot read", alicesDefault(), bob, acl.PermRead, false),
		Entry("not even the owner holds PermInvalid", alicesDefault(), alice, acl.PermInvalid, false),
		Entry("the owner reads the ACL with no grant", alicesPolicy(), alice, acl.PermReadACP, true),
		Entry("the owner writes the ACL with no grant", alicesPolicy(), alice, acl.PermWriteACP, true),
		Entry("the owner cannot read with no grant", alicesPolicy(), alice, acl.PermRead, false),
		Entry("the owner cannot write with no grant", alicesPolicy(), alice, acl.PermWrite, false),
	)
	DescribeTable("the AllUsers and AuthenticatedUsers groups",
		func(g acl.Grant, id fakeIdentity, perm acl.Permission, ignorePublicACLs, want bool) {
			p := alicesPolicy(userGrant("alice", acl.PermFullControl), g)
			Expect(p.Verify(id, acl.PermFullControl, perm, "", ignorePublicACLs)).To(Equal(want), "perm %#x", perm)
		},
		Entry("AllUsers READ lets a stranger read",
			groupGrant(acl.GroupAllUsers, acl.PermRead), bob, acl.PermRead, false, true),
		Entry("AllUsers READ lets the anonymous user read",
			groupGrant(acl.GroupAllUsers, acl.PermRead), anonymous, acl.PermRead, false, true),
		Entry("AllUsers READ does not let the anonymous user write",
			groupGrant(acl.GroupAllUsers, acl.PermRead), anonymous, acl.PermWrite, false, false),
		Entry("AuthenticatedUsers READ lets a stranger read",
			groupGrant(acl.GroupAuthenticatedUsers, acl.PermRead), bob, acl.PermRead, false, true),
		Entry("AuthenticatedUsers READ does not let the anonymous user read",
			groupGrant(acl.GroupAuthenticatedUsers, acl.PermRead), anonymous, acl.PermRead, false, false),
		Entry("IgnorePublicAcls sets AllUsers aside",
			groupGrant(acl.GroupAllUsers, acl.PermRead), bob, acl.PermRead, true, false),
		Entry("IgnorePublicAcls sets AuthenticatedUsers aside",
			groupGrant(acl.GroupAuthenticatedUsers, acl.PermRead), bob, acl.PermRead, true, false),
		Entry("IgnorePublicAcls leaves the owner's own grant",
			groupGrant(acl.GroupAllUsers, acl.PermRead), alice, acl.PermRead, true, true),
	)
	DescribeTable("the requester's permission mask",
		func(userPermMask, perm acl.Permission, want bool) {
			Expect(alicesDefault().Verify(alice, userPermMask, perm, "", false)).To(Equal(want), "perm %#x", perm)
		},
		Entry("a READ mask refuses the WRITE a FULL_CONTROL grant gives", acl.PermRead, acl.PermWrite, false),
		Entry("a READ mask passes READ", acl.PermRead, acl.PermRead, true),
	)
	DescribeTable("Swift's container flags",
		func(flags, perm acl.Permission, want bool) {
			p := alicesPolicy(userGrant("bob", flags))
			Expect(p.Verify(bob, acl.PermFullControl, perm, "", false)).To(Equal(want), "perm %#x", perm)
		},
		Entry("WRITE_OBJS gives WRITE", acl.PermWriteObjs, acl.PermWrite, true),
		Entry("WRITE_OBJS gives WRITE_ACP", acl.PermWriteObjs, acl.PermWriteACP, true),
		Entry("WRITE_OBJS does not give READ", acl.PermWriteObjs, acl.PermRead, false),
		Entry("READ_OBJS gives READ", acl.PermReadObjs, acl.PermRead, true),
		Entry("READ_OBJS gives READ_ACP", acl.PermReadObjs, acl.PermReadACP, true),
		Entry("READ_OBJS does not give WRITE", acl.PermReadObjs, acl.PermWrite, false),
	)
	DescribeTable("referer grants, for a stranger's READ",
		func(grants []acl.Grant, referer string, ignorePublicACLs, want bool) {
			Expect(alicesPolicy(grants...).Verify(bob, acl.PermFullControl, acl.PermRead, referer, ignorePublicACLs)).
				To(Equal(want), "referer %q", referer)
		},
		Entry("a host under a dotted spec reads",
			[]acl.Grant{refererGrant(".example.com", acl.PermRead)}, "https://a.example.com/x", false, true),
		Entry("a host outside the spec does not",
			[]acl.Grant{refererGrant(".example.com", acl.PermRead)}, "https://example.org/", false, false),
		Entry("the domain a dotted spec names does not",
			[]acl.Grant{refererGrant(".example.com", acl.PermRead)}, "https://example.com/", false, false),
		Entry("a request without a Referer does not",
			[]acl.Grant{refererGrant(".example.com", acl.PermRead)}, "", false, false),
		Entry("IgnorePublicAcls sets a grant to one domain aside",
			[]acl.Grant{refererGrant(".example.com", acl.PermRead)}, "https://a.example.com/x", true, false),
		Entry("IgnorePublicAcls sets an exact host's grant aside",
			[]acl.Grant{refererGrant("example.com", acl.PermRead)}, "https://example.com/", true, false),
		Entry("IgnorePublicAcls sets the wildcard aside for a request with a Referer",
			[]acl.Grant{refererGrant("*", acl.PermRead)}, "https://example.com/", true, false),
		Entry("IgnorePublicAcls sets the wildcard aside for a request without one",
			[]acl.Grant{refererGrant("*", acl.PermRead)}, "", true, false),
		Entry("the wildcard reads without a Referer, through the AllUsers group it registers",
			[]acl.Grant{refererGrant("*", acl.PermRead)}, "", false, true),
		Entry("a matching negative grant revokes what AllUsers gave",
			[]acl.Grant{groupGrant(acl.GroupAllUsers, acl.PermRead), refererGrant(".evil.com", 0)},
			"https://x.evil.com/", false, false),
		Entry("a negative grant that does not match leaves what AllUsers gave",
			[]acl.Grant{groupGrant(acl.GroupAllUsers, acl.PermRead), refererGrant(".evil.com", 0)},
			"https://x.example.com/", false, true),
	)
	It("lets a matching referer grant replace even the owner's own grant, as radosgw does", func() {
		// A Swift .r: read grant carries READ_OBJS, and the bucket path passes
		// perm as the mask
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1420-L1423).
		p := alicesPolicy(userGrant("alice", acl.PermFullControl), refererGrant("example.com", acl.PermReadObjs))
		Expect(p.Verify(alice, acl.PermWrite, acl.PermWrite, "http://example.com/page", false)).
			To(BeFalse(), "with a matching Referer")
		Expect(p.Verify(alice, acl.PermWrite, acl.PermWrite, "", false)).To(BeTrue(), "without a Referer")
	})
	It("lets an account user read through a grant to its account", func() {
		p := alicesPolicy(userGrant("RGW12345678901234567", acl.PermRead))
		Expect(p.Verify(carol, acl.PermFullControl, acl.PermRead, "", false)).To(BeTrue(), "carol, of the account")
		Expect(p.Verify(bob, acl.PermFullControl, acl.PermRead, "", false)).To(BeFalse(), "bob, of no account")
	})
	// Every unauthenticated request runs as the user anonymous
	// (rgw_get_anon_user,
	// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_user.cc#L106-L111 at v19.2.7 and v20.2.4),
	// so a grant to that user, and its ownership, admit everyone.
	DescribeTable("the anonymous user's grants and ownership, for the anonymous requester",
		func(p acl.Policy, perm acl.Permission, ignorePublicACLs, want bool) {
			Expect(p.Verify(anonymous, perm, perm, "", ignorePublicACLs)).
				To(Equal(want), "perm %#x, IgnorePublicAcls %t", perm, ignorePublicACLs)
		},
		Entry("a grant to the user reads",
			alicesPolicy(userGrant("anonymous", acl.PermRead)), acl.PermRead, false, true),
		Entry("IgnorePublicAcls sets a grant to the user aside",
			alicesPolicy(userGrant("anonymous", acl.PermRead)), acl.PermRead, true, false),
		Entry("Swift's READ_OBJS item for the user gives the fallback's read",
			alicesPolicy(userGrant("anonymous", acl.PermReadObjs)), acl.PermReadObjs, false, true),
		Entry("IgnorePublicAcls sets Swift's READ_OBJS item for the user aside",
			alicesPolicy(userGrant("anonymous", acl.PermReadObjs)), acl.PermReadObjs, true, false),
		Entry("Swift's WRITE_OBJS item for the user gives a write",
			alicesPolicy(userGrant("anonymous", acl.PermWriteObjs)), acl.PermWrite, false, true),
		Entry("IgnorePublicAcls sets Swift's WRITE_OBJS item for the user aside",
			alicesPolicy(userGrant("anonymous", acl.PermWriteObjs)), acl.PermWrite, true, false),
		Entry("an email grant keyed like the user reads, as the user map holds it",
			alicesPolicy(emailGrant("anonymous", acl.PermRead)), acl.PermRead, false, true),
		Entry("IgnorePublicAcls sets the email grant aside too",
			alicesPolicy(emailGrant("anonymous", acl.PermRead)), acl.PermRead, true, false),
		Entry("the user's ownership gives READ_ACP",
			acl.Policy{Owner: acl.Owner{ID: "anonymous"}}, acl.PermReadACP, false, true),
		Entry("IgnorePublicAcls sets the user's ownership aside for READ_ACP",
			acl.Policy{Owner: acl.Owner{ID: "anonymous"}}, acl.PermReadACP, true, false),
		Entry("the user's ownership gives WRITE_ACP",
			acl.Policy{Owner: acl.Owner{ID: "anonymous"}}, acl.PermWriteACP, false, true),
		Entry("IgnorePublicAcls sets the user's ownership aside for WRITE_ACP",
			acl.Policy{Owner: acl.Owner{ID: "anonymous"}}, acl.PermWriteACP, true, false),
	)
	It("leaves under IgnorePublicAcls the grants and ownership of a user who is not the anonymous one", func() {
		p := acl.Policy{Owner: acl.Owner{ID: "bob"}}
		p.ACL.AddGrant(userGrant("bob", acl.PermRead))
		p.ACL.AddGrant(userGrant("anonymous", acl.PermRead))
		Expect(p.Verify(bob, acl.PermFullControl, acl.PermRead, "", true)).To(BeTrue(), "bob's own grant")
		Expect(p.Verify(bob, acl.PermFullControl, acl.PermWriteACP, "", true)).To(BeTrue(), "bob's ownership")
		Expect(p.Verify(anonymous, acl.PermFullControl, acl.PermRead, "", true)).To(BeFalse(), "the anonymous grant beside them")
		Expect(p.ACL.UserMap).To(HaveKeyWithValue("anonymous", int32(acl.PermRead)), "the stored map, untouched")
	})
	It("answers under IgnorePublicAcls as for a request without a Referer, a negative grant's revoked flags included", func() {
		// The client chooses whether to send a Referer, so a request that
		// leaves out the one a negative grant matches gets what that grant
		// revokes in any case.
		p := alicesPolicy(userGrant("bob", acl.PermRead), refererGrant(".evil.com", 0))
		Expect(p.Verify(bob, acl.PermFullControl, acl.PermRead, "https://x.evil.com/", false)).
			To(BeFalse(), "the negative grant revokes bob's READ")
		Expect(p.Verify(bob, acl.PermFullControl, acl.PermRead, "", false)).
			To(BeTrue(), "bob reads without the Referer it matches")
		Expect(p.Verify(bob, acl.PermFullControl, acl.PermRead, "https://x.evil.com/", true)).
			To(BeTrue(), "under IgnorePublicAcls, as without the Referer")
	})
})

// swiftContainer is the policy rgw::swift::create_container_policy builds
// for alice's container from an X-Container-Read list of .r: items, each
// given by its spec
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl_swift.cc#L172-L208 at v19.2.7 and v20.2.4):
// create_default's FULL_CONTROL grant to alice, then a referer grant of
// READ_OBJS for each spec, or of nothing for a spec starting with "-"
// (referrer_to_grant,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl_swift.cc#L57-L96 at v19.2.7 and v20.2.4).
func swiftContainer(specs ...string) acl.Policy {
	grants := []acl.Grant{userGrant("alice", acl.PermFullControl)}
	for _, s := range specs {
		perm := acl.PermReadObjs
		if spec, negative := strings.CutPrefix(s, "-"); negative {
			s, perm = spec, acl.PermNone
		}
		grants = append(grants, refererGrant(s, perm))
	}
	return alicesPolicy(grants...)
}

var _ = Describe("a Swift container's referer grants", func() {
	// The Swift fallback of verify_object_permission_no_policy asks the
	// bucket's ACL for READ_OBJS with the request's Referer
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1598-L1599 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1655-L1656 at v20.2.4);
	// the bucket check asks for READ.
	DescribeTable("for the anonymous user",
		func(p acl.Policy, perm acl.Permission, referer string, ignorePublicACLs, want bool) {
			Expect(p.Verify(anonymous, perm, perm, referer, ignorePublicACLs)).
				To(Equal(want), "perm %#x, referer %q, IgnorePublicAcls %t", perm, referer, ignorePublicACLs)
		},
		Entry(".r:* gives an object read without a Referer",
			swiftContainer("*"), acl.PermReadObjs, "", false, true),
		Entry(".r:* gives an object read with a Referer",
			swiftContainer("*"), acl.PermReadObjs, "http://example.com/", false, true),
		Entry(".r:* gives a listing with a Referer",
			swiftContainer("*"), acl.PermRead, "http://example.com/", false, true),
		Entry(".r:* gives no object read under IgnorePublicAcls, without a Referer",
			swiftContainer("*"), acl.PermReadObjs, "", true, false),
		Entry(".r:* gives no object read under IgnorePublicAcls, with a Referer",
			swiftContainer("*"), acl.PermReadObjs, "http://example.com/", true, false),
		Entry(".r:* gives no listing under IgnorePublicAcls, with a Referer",
			swiftContainer("*"), acl.PermRead, "http://example.com/", true, false),
		Entry(".r:.example.com gives an object read to a matching Referer",
			swiftContainer(".example.com"), acl.PermReadObjs, "http://a.example.com/", false, true),
		Entry(".r:.example.com gives no object read under IgnorePublicAcls",
			swiftContainer(".example.com"), acl.PermReadObjs, "http://a.example.com/", true, false),
		Entry(".r:.example.com gives no listing under IgnorePublicAcls",
			swiftContainer(".example.com"), acl.PermRead, "http://a.example.com/", true, false),
	)

	It("leave the answer under IgnorePublicAcls the same whatever the Referer", func() {
		policies := map[string]acl.Policy{
			".r:*":                        swiftContainer("*"),
			".r:.example.com":             swiftContainer(".example.com"),
			".r:*,.r:-.evil.com":          swiftContainer("*", "-.evil.com"),
			"bob READ, .r:-.evil.com":     alicesPolicy(userGrant("bob", acl.PermRead), refererGrant(".evil.com", 0)),
			"AllUsers READ, .r:-evil.com": alicesPolicy(groupGrant(acl.GroupAllUsers, acl.PermRead), refererGrant("evil.com", 0)),
		}
		ids := map[string]fakeIdentity{"alice": alice, "bob": bob, "anonymous": anonymous, "carol": carol}
		perms := []acl.Permission{acl.PermRead, acl.PermWrite, acl.PermReadACP, acl.PermWriteACP, acl.PermReadObjs, acl.PermWriteObjs}
		referers := []string{"http://example.com/", "http://a.example.com/", "http://x.evil.com/", "http://evil.com/"}
		for name, p := range policies {
			for who, id := range ids {
				for _, perm := range perms {
					want := p.Verify(id, acl.PermFullControl|perm, perm, "", true)
					for _, ref := range referers {
						Expect(p.Verify(id, acl.PermFullControl|perm, perm, ref, true)).
							To(Equal(want), "%s, %s, perm %#x, referer %q", name, who, perm, ref)
					}
				}
			}
		}
	})
})

var _ = Describe("Policy.IsPublic", func() {
	DescribeTable("is whether the ACL holds a grant IgnorePublicAcls sets aside",
		func(p acl.Policy, want bool) {
			Expect(p.IsPublic()).To(Equal(want), "group map %v, referers %v", p.ACL.GroupMap, p.ACL.RefererList)
		},
		Entry("AllUsers READ", alicesPolicy(groupGrant(acl.GroupAllUsers, acl.PermRead)), true),
		Entry("AuthenticatedUsers WRITE", alicesPolicy(groupGrant(acl.GroupAuthenticatedUsers, acl.PermWrite)), true),
		Entry("the wildcard referer, registered as AllUsers", alicesPolicy(refererGrant("*", acl.PermRead)), true),
		Entry("AllUsers with only Swift's READ_OBJS",
			alicesPolicy(groupGrant(acl.GroupAllUsers, acl.PermReadObjs)), true),
		Entry("AuthenticatedUsers with only Swift's WRITE_OBJS",
			alicesPolicy(groupGrant(acl.GroupAuthenticatedUsers, acl.PermWriteObjs)), true),
		Entry("AllUsers with only flags beyond Swift's",
			alicesPolicy(groupGrant(acl.GroupAllUsers, acl.PermInvalid)), true),
		Entry("Swift's .r:*", swiftContainer("*"), true),
		Entry("only a referer grant to one domain", swiftContainer(".example.com"), true),
		Entry("only a negative referer grant", swiftContainer("-.evil.com"), true),
		Entry("the owner's default", alicesDefault(), false),
		Entry("only user and email grants",
			alicesPolicy(userGrant("alice", acl.PermFullControl), emailGrant("b@example.com", acl.PermRead)), false),
		Entry("a user grant of Swift's flags",
			alicesPolicy(userGrant("bob", acl.PermReadObjs|acl.PermWriteObjs)), false),
		Entry("a group grant with no flags", alicesPolicy(groupGrant(acl.GroupAllUsers, 0)), false),
		Entry("a grant to no group, with every flag",
			alicesPolicy(groupGrant(acl.GroupNone, acl.PermFullControl|acl.PermReadObjs|acl.PermWriteObjs)), false),
		Entry("an unknown grantee with every flag",
			alicesPolicy(acl.Grant{Type: acl.GranteeUnknown, Permission: acl.PermFullControl}), false),
		Entry("Swift's read item for the user anonymous",
			alicesPolicy(userGrant("anonymous", acl.PermReadObjs)), true),
		Entry("Swift's write item for the user anonymous",
			alicesPolicy(userGrant("anonymous", acl.PermWriteObjs)), true),
		Entry("an S3 grant to the user anonymous", alicesPolicy(userGrant("anonymous", acl.PermRead)), true),
		Entry("an email grant keyed like the user anonymous", alicesPolicy(emailGrant("anonymous", acl.PermRead)), true),
		Entry("a policy the user anonymous owns", acl.Policy{Owner: acl.Owner{ID: "anonymous"}}, true),
		Entry("the policy Canned builds for an anonymous writer, as stored",
			cannedFor(anonOwner, ""), true),
		Entry("a grant to the user anonymous with no flags", alicesPolicy(userGrant("anonymous", 0)), false),
		Entry("a grant to a tenant's user named anonymous", alicesPolicy(userGrant("t1$anonymous", acl.PermRead)), false),
		Entry("a policy a tenant's user named anonymous owns", acl.Policy{Owner: acl.Owner{ID: "t1$anonymous"}}, false),
	)

	It("calls private only an ACL every answer of which IgnorePublicAcls leaves as it is", func() {
		private := map[string]acl.Policy{
			"the owner's default":         alicesDefault(),
			"no grant":                    alicesPolicy(),
			"user and email grants":       alicesPolicy(userGrant("bob", acl.PermRead), emailGrant("b@example.com", acl.PermWrite)),
			"a user grant of Swift flags": alicesPolicy(userGrant("bob", acl.PermReadObjs|acl.PermWriteObjs)),
			"an account grant":            alicesPolicy(userGrant("RGW12345678901234567", acl.PermFullControl)),
			"group grants of nothing":     alicesPolicy(groupGrant(acl.GroupAllUsers, 0), groupGrant(acl.GroupAuthenticatedUsers, 0)),
			"a grant to no group":         alicesPolicy(groupGrant(acl.GroupNone, acl.PermFullControl)),
			"a tenant's user named anonymous": {
				Owner: acl.Owner{ID: "t1$anonymous"},
				ACL:   alicesPolicy(userGrant("t1$anonymous", acl.PermFullControl)).ACL,
			},
		}
		ids := map[string]fakeIdentity{"alice": alice, "bob": bob, "anonymous": anonymous, "carol": carol}
		perms := []acl.Permission{
			acl.PermRead, acl.PermWrite, acl.PermReadACP, acl.PermWriteACP, acl.PermFullControl,
			acl.PermReadObjs, acl.PermWriteObjs, acl.PermInvalid,
		}
		referers := []string{"", "http://example.com/", "http://a.example.com/"}
		for name, p := range private {
			Expect(p.IsPublic()).To(BeFalse(), name)
			for who, id := range ids {
				for _, perm := range perms {
					for _, ref := range referers {
						Expect(p.Verify(id, perm, perm, ref, true)).To(Equal(p.Verify(id, perm, perm, ref, false)),
							"%s, %s, perm %#x, referer %q", name, who, perm, ref)
					}
				}
			}
		}
	})

	It("answers as radosgw's is_public for every ACL an S3 request builds that gives the user anonymous nothing", func(ctx SpecContext) {
		built := map[string]acl.Policy{}
		for _, canned := range []string{
			"", "private", "public-read", "public-read-write", "authenticated-read",
			"bucket-owner-read", "bucket-owner-full-control",
		} {
			for _, owner := range []acl.Owner{aliceOwner, bobOwner} {
				p, err := acl.Canned(owner, aliceOwner, canned)
				Expect(err).NotTo(HaveOccurred(), "canned %q", canned)
				built["canned "+canned+" by "+owner.ID] = p
			}
		}
		headers := map[string]func(*acl.GrantHeaders, string){
			"x-amz-grant-read":         func(h *acl.GrantHeaders, v string) { h.Read = v },
			"x-amz-grant-write":        func(h *acl.GrantHeaders, v string) { h.Write = v },
			"x-amz-grant-read-acp":     func(h *acl.GrantHeaders, v string) { h.ReadACP = v },
			"x-amz-grant-write-acp":    func(h *acl.GrantHeaders, v string) { h.WriteACP = v },
			"x-amz-grant-full-control": func(h *acl.GrantHeaders, v string) { h.FullControl = v },
		}
		uris := []string{acl.URIAllUsers, acl.URIAuthenticatedUsers}
		items := []string{`id="bob"`, `emailAddress="c@x"`, `uri="` + uris[0] + `"`, `uri="` + uris[1] + `"`}
		for name, set := range headers {
			for _, item := range items {
				var h acl.GrantHeaders
				set(&h, item)
				p, err := acl.FromHeaders(ctx, newResolver(), aliceOwner, h)
				Expect(err).NotTo(HaveOccurred(), "%s: %s", name, item)
				built[name+": "+item] = p
			}
		}
		for _, perm := range []string{"READ", "WRITE", "READ_ACP", "WRITE_ACP", "FULL_CONTROL", "read"} {
			for _, uri := range uris {
				doc := aclDoc(xmlGrant("CanonicalUser", "<ID>alice</ID>", "FULL_CONTROL"),
					xmlGrant("Group", "<URI>"+uri+"</URI>", perm))
				p, err := acl.ParseS3XML(ctx, newResolver(), []byte(doc))
				Expect(err).NotTo(HaveOccurred(), doc)
				built["xml "+perm+" to "+uri] = p
			}
		}
		var public, private int
		for name, p := range built {
			want := radosgwIsPublic(p)
			Expect(p.IsPublic()).To(Equal(want), name)
			if want {
				public++
			} else {
				private++
			}
		}
		Expect([]int{public, private}).To(HaveEach(BeNumerically(">", 0)), "public and private ACLs among those built")
	})

	Describe("for the ACL an anonymous writer's own write builds", func() {
		It("does not count the writer's own rights, so it answers as radosgw's is_public", func(ctx SpecContext) {
			built := map[string]acl.Policy{}
			for _, canned := range []string{
				"", "private", "public-read", "public-read-write", "authenticated-read",
				"bucket-owner-read", "bucket-owner-full-control",
			} {
				built["canned "+canned] = cannedFor(anonOwner, canned)
			}
			for _, item := range []string{`id="bob"`, `emailAddress="c@x"`, `uri="` + acl.URIAllUsers + `"`} {
				p, err := acl.FromHeaders(ctx, newResolver(), anonOwner, acl.GrantHeaders{Read: item})
				Expect(err).NotTo(HaveOccurred(), item)
				built["x-amz-grant-read: "+item] = p
			}
			var public, private int
			for name, p := range built {
				Expect(p.IsPublic()).To(BeTrue(), "%s, as stored", name)
				want := radosgwIsPublic(p)
				Expect(p.ForOwnWrite(anonOwner).IsPublic()).To(Equal(want), "%s, as its own write", name)
				if want {
					public++
				} else {
					private++
				}
			}
			Expect([]int{public, private}).To(HaveEach(BeNumerically(">", 0)), "public and private ACLs among those built")
		})

		It("still counts a grant to the user anonymous the write names", func(ctx SpecContext) {
			res := newResolver()
			res.names["anonymous"] = ""
			p, err := acl.FromHeaders(ctx, res, anonOwner, acl.GrantHeaders{Read: `id="anonymous"`})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.ForOwnWrite(anonOwner).IsPublic()).To(BeTrue(), "a header grant to the user anonymous")

			q := cannedFor(anonOwner, "").ForOwnWrite(anonOwner)
			q.ACL.AddGrant(userGrant("anonymous", acl.PermReadObjs))
			Expect(q.IsPublic()).To(BeTrue(), "a flag beyond the canned FULL_CONTROL")
		})

		It("is not encoded, so the stored policy decodes public", func() {
			p := cannedFor(anonOwner, "").ForOwnWrite(anonOwner)
			Expect(p.IsPublic()).To(BeFalse(), "as its own write")
			Expect(decodeWhole(encodeAt(p, denc.Squid), acl.DecodePolicy).IsPublic()).To(BeTrue(), "decoded")
		})

		It("leaves a policy whose writer is not the anonymous user as it is", func() {
			for _, p := range []acl.Policy{
				cannedFor(anonOwner, ""), swiftContainer("*"), alicesPolicy(userGrant("anonymous", acl.PermRead)),
			} {
				Expect(p.ForOwnWrite(aliceOwner)).To(Equal(p), "grants %v", p.ACL.Grants)
			}
		})
	})
})

// radosgwIsPublic is RGWAccessControlPolicy::is_public, which masks the two
// public groups' flags with FULL_CONTROL
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L235-L247 at v19.2.7 and v20.2.4).
func radosgwIsPublic(p acl.Policy) bool {
	return p.ACL.GroupPerm(acl.GroupAllUsers, acl.PermFullControl) != acl.PermNone ||
		p.ACL.GroupPerm(acl.GroupAuthenticatedUsers, acl.PermFullControl) != acl.PermNone
}

// anonOwner is the anonymous user as an ACL owner, rgw_get_anon_user's id.
var anonOwner = acl.Owner{ID: "anonymous"}

// cannedFor is the canned ACL canned that owner's write builds in alice's
// bucket.
func cannedFor(owner acl.Owner, canned string) acl.Policy {
	GinkgoHelper()
	p, err := acl.Canned(owner, aliceOwner, canned)
	Expect(err).NotTo(HaveOccurred(), "canned %q", canned)
	return p
}

var _ = Describe("List.GroupPerm", func() {
	DescribeTable("is the group's stored flags within the mask",
		func(group uint32, mask, want acl.Permission) {
			l := acl.List{GroupMap: map[uint32]int32{acl.GroupAllUsers: 3, acl.GroupAuthenticatedUsers: -1}}
			Expect(l.GroupPerm(group, mask)).To(Equal(want))
		},
		Entry("flags outside the mask dropped", acl.GroupAllUsers, acl.PermRead|acl.PermReadACP, acl.PermRead),
		Entry("a negative stored value as its bits", acl.GroupAuthenticatedUsers, acl.PermFullControl, acl.PermFullControl),
		Entry("nothing for a group without an entry", acl.GroupNone, acl.PermFullControl, acl.PermNone),
	)
})

var _ = Describe("List.RefererPerm", func() {
	DescribeTable("replaces the current flags with the last matching grant's, masked",
		func(refs []acl.Referer, current acl.Permission, referer string, want acl.Permission) {
			l := acl.List{RefererList: refs}
			Expect(l.RefererPerm(current, referer, acl.PermRead|acl.PermWrite)).To(Equal(want))
		},
		Entry("no match keeps the current flags",
			[]acl.Referer{{URLSpec: ".example.com", Perm: 1}}, acl.PermWrite|acl.PermReadACP,
			"http://example.org/", acl.PermWrite),
		Entry("a later match overrides an earlier one",
			[]acl.Referer{{URLSpec: "*", Perm: 3}, {URLSpec: ".example.com", Perm: 1}}, acl.PermNone,
			"http://a.example.com/", acl.PermRead),
		Entry("a match with no flags revokes the current ones",
			[]acl.Referer{{URLSpec: ".example.com", Perm: 0}}, acl.PermWrite,
			"http://a.example.com/", acl.PermNone),
	)
})

var _ = Describe("Referer.IsMatch", func() {
	DescribeTable("matches the Referer's host as ACLReferer::is_match does",
		func(spec, referer string, want bool) {
			Expect(acl.Referer{URLSpec: spec}.IsMatch(referer)).To(Equal(want), "spec %q, referer %q", spec, referer)
		},
		Entry("the wildcard matches any host", "*", "http://example.com/", true),
		Entry("the wildcard needs a host at least as long as itself", "*", "http:///x", false),
		Entry("a spec equal to the host", "example.com", "https://example.com/x", true),
		Entry("a port ends the host", "example.com", "http://example.com:8080/", true),
		Entry("userinfo before an @ is not the host", "example.com", "http://user:pw@example.com/", true),
		Entry("an undotted spec does not match a host under it", "example.com", "http://a.example.com/", false),
		Entry("a dotted spec matches a host under it, with no path", ".example.com", "http://a.b.example.com", true),
		Entry("a dotted spec does not match the domain it names", ".example.com", "http://example.com/", false),
		Entry("a dotted spec matches only at a dot", ".example.com", "http://aexample.com/", false),
		Entry("a value without :// has no host", "*", "example.com", false),
		Entry("a value starting with :// has no host", "*", "://example.com/", false),
		Entry("a value ending with :// has no host", "*", "http://", false),
		Entry("a value ending with @ has no host", "*", "http://example.com/@", false),
		Entry("the first @ after the scheme ends the userinfo, even in the path",
			"evil.com", "http://example.com/x@evil.com", true),
		Entry("an empty spec does not match a host", "", "http://example.com/", false),
		Entry("an empty spec matches an empty host", "", "http:///x", true),
	)
})
