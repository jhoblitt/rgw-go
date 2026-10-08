package op_test

import (
	"context"
	"errors"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// publicReadOf is alice's policy with READ granted to AllUsers.
func publicReadOf(owner meta.Owner) acl.Policy {
	p := acl.DefaultPolicy(owner, "Alice")
	p.ACL.AddGrant(acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermRead})
	return p
}

var _ = Describe("PutObjectACL", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	DescribeTable("is radosgw's put_acls at object scope, a write of the action the instance selects",
		func(ctx SpecContext, instance string, want policy.Action, wantErr error) {
			fake := &opfakes.FakeAuthorizer{}
			f.env.Authz = fake
			o := &op.PutObjectACL{Build: func(p acl.Policy) (acl.Policy, error) { return p, nil }}
			r := f.req(http.MethodPut, "plain", "small")
			r.Object.Instance = instance
			err := op.Run(ctx, o, r)
			if wantErr == nil {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(wantErr))
			}
			Expect(o.Name()).To(Equal("put_acls"))
			Expect(o.OpMask()).To(Equal(op.OpTypeWrite))
			Expect(o.Action()).To(Equal(want))
			_, _, a, perm := fake.VerifyObjectArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
		},
		Entry("no instance", "", policy.S3PutObjectAcl, nil),
		Entry("the null instance, the plain object on a bucket whose versioning was never enabled", "null", policy.S3PutObjectVersionAcl, nil),
		Entry("a version, which is not served yet", "v1", policy.S3PutObjectVersionAcl, op.ErrNotImplemented),
	)
	It("builds the new policy from the stored one and stores it, keeping every other attr", func(ctx SpecContext) {
		before := f.stat(ctx, "small")
		var existing acl.Policy
		newP := publicReadOf(f.alice.Owner)
		o := &op.PutObjectACL{Build: func(p acl.Policy) (acl.Policy, error) {
			existing = p
			return newP, nil
		}}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "small"))).To(Succeed())
		Expect(encodedPolicy(existing)).To(Equal(f.aliceACL))
		after := f.stat(ctx, "small")
		Expect(after.Attrs).To(HaveKeyWithValue(meta.AttrACL, encodedPolicy(newP)))
		for k, v := range before.Attrs {
			if k != meta.AttrACL {
				Expect(after.Attrs).To(HaveKeyWithValue(k, v))
			}
		}
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("passes the object's whole attr set with the ACL replaced to the store", func(ctx SpecContext) {
		stub := &opfakes.FakeObjectStore{}
		st := &op.ObjectState{Exists: true, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL, meta.AttrETag: []byte("e"), tags.Attr: []byte("t")}}
		stub.StatObjectReturns(st, nil)
		f.env.Objects = stub
		newP := publicReadOf(f.alice.Owner)
		Expect(op.Run(ctx, &op.PutObjectACL{Build: func(acl.Policy) (acl.Policy, error) { return newP, nil }}, f.req(http.MethodPut, "plain", "small"))).To(Succeed())
		_, gotSt, set, rm := stub.SetObjectAttrsArgsForCall(0)
		Expect(gotSt).To(BeIdenticalTo(st))
		Expect(set).To(Equal(map[string][]byte{meta.AttrACL: encodedPolicy(newP), meta.AttrETag: []byte("e"), tags.Attr: []byte("t")}))
		Expect(rm).To(BeEmpty())
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrACL, f.aliceACL), "the state's own attrs are not changed")
	})
	It("gives Build the default policy of the bucket's owner for an object without an ACL", func(ctx SpecContext) {
		_, err := f.store.PutObject(ctx, f.rec, meta.ObjKey{Name: "bare"}, http.NoBody, op.PutParams{Size: 0})
		Expect(err).NotTo(HaveOccurred())
		var existing acl.Policy
		o := &op.PutObjectACL{Build: func(p acl.Policy) (acl.Policy, error) {
			existing = p
			return p, nil
		}}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "bare"))).To(Succeed())
		Expect(encodedPolicy(existing)).To(Equal(f.aliceACL))
	})
	It("returns Build's error and stores nothing", func(ctx SpecContext) {
		o := &op.PutObjectACL{Build: func(acl.Policy) (acl.Policy, error) {
			return acl.Policy{}, op.ErrAccessDenied.WithMessage("Cannot modify ACL Owner")
		}}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "small"))).To(MatchError(op.ErrAccessDenied))
		Expect(f.stat(ctx, "small").Attrs).To(HaveKeyWithValue(meta.AttrACL, f.aliceACL))
	})
	It("refuses a public policy under a block of public ACLs", func(ctx SpecContext) {
		f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
		o := &op.PutObjectACL{Build: func(acl.Policy) (acl.Policy, error) { return publicReadOf(f.alice.Owner), nil }}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "small"))).To(MatchError(op.ErrAccessDenied))
		Expect(f.stat(ctx, "small").Attrs).To(HaveKeyWithValue(meta.AttrACL, f.aliceACL))
	})
	It("stores a private policy under a block of public ACLs", func(ctx SpecContext) {
		f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
		o := &op.PutObjectACL{Build: func(p acl.Policy) (acl.Policy, error) { return p, nil }}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "small"))).To(Succeed())
	})
	Describe("a write that loses a race", func() {
		var stub *opfakes.FakeObjectStore
		keep := func(p acl.Policy) (acl.Policy, error) { return p, nil }
		BeforeEach(func() {
			stub = &opfakes.FakeObjectStore{}
			stub.StatObjectReturns(&op.ObjectState{Exists: true, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL}}, nil)
			f.env.Objects = stub
		})
		It("is retried after the head is read again, and answers ConcurrentModification after the last try, where radosgw answers success", func(ctx SpecContext) {
			stub.SetObjectAttrsReturns(op.ErrConcurrentModification)
			Expect(op.Run(ctx, &op.PutObjectACL{Build: keep}, f.req(http.MethodPut, "plain", "small"))).To(MatchError(op.ErrConcurrentModification),
				"rgw_op.cc:5925-5927 at v19.2.6 answers 0")
			Expect(stub.SetObjectAttrsCallCount()).To(Equal(16), "the first try and retry_raced_bucket_write's fifteen")
			Expect(stub.StatObjectCallCount()).To(Equal(16), "Init's read and one before each retry")
		})
		It("stores the ACL over the head the winning retry read", func(ctx SpecContext) {
			reread := &op.ObjectState{Exists: true, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL, meta.AttrETag: []byte("new")}}
			stub.StatObjectReturnsOnCall(1, reread, nil)
			stub.SetObjectAttrsReturnsOnCall(0, op.ErrConcurrentModification)
			Expect(op.Run(ctx, &op.PutObjectACL{Build: keep}, f.req(http.MethodPut, "plain", "small"))).To(Succeed())
			Expect(stub.SetObjectAttrsCallCount()).To(Equal(2))
			_, st, set, _ := stub.SetObjectAttrsArgsForCall(1)
			Expect(st).To(BeIdenticalTo(reread))
			Expect(set).To(HaveKeyWithValue(meta.AttrETag, []byte("new")), "the retry writes back the attrs it read")
		})
		It("refuses a retry once another write gave the object another owner", func(ctx SpecContext) {
			stub.StatObjectReturnsOnCall(1, &op.ObjectState{Exists: true, Attrs: map[string][]byte{
				meta.AttrACL: encodedPolicy(acl.DefaultPolicy(f.bob.Owner, "Bob")),
			}}, nil)
			stub.SetObjectAttrsReturnsOnCall(0, op.ErrConcurrentModification)
			err := op.Run(ctx, &op.PutObjectACL{Build: keep}, f.req(http.MethodPut, "plain", "small"))
			Expect(err).To(MatchError(op.ErrAccessDenied))
			Expect(op.AsError(err).Message).To(Equal("Cannot modify ACL Owner"))
			Expect(stub.SetObjectAttrsCallCount()).To(Equal(1))
		})
		It("refuses a public policy on a retry once the bucket blocks public ACLs", func(ctx SpecContext) {
			stub.SetObjectAttrsStub = func(context.Context, *op.ObjectState, map[string][]byte, []string) error {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
				return op.ErrConcurrentModification
			}
			o := &op.PutObjectACL{Build: func(acl.Policy) (acl.Policy, error) { return publicReadOf(f.alice.Owner), nil }}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "small"))).To(MatchError(op.ErrAccessDenied))
			Expect(stub.SetObjectAttrsCallCount()).To(Equal(1))
		})
		Describe("re-authorized on every retry against the head it read", func() {
			// bobCanWriteACP is alice's policy with WRITE_ACP granted to bob.
			bobCanWriteACP := func() acl.Policy {
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP})
				return p
			}
			// race makes the first write lose to a concurrent write of the
			// object's ACL as concurrent, then lets the retries through to the store.
			race := func(ctx context.Context, concurrent acl.Policy) {
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				st := f.stat(ctx, "small")
				Expect(f.store.SetObjectAttrs(ctx, st, map[string][]byte{meta.AttrACL: encodedPolicy(bobCanWriteACP())}, nil)).To(Succeed())
				stub.StatObjectStub = f.store.StatObject
				stub.SetObjectAttrsStub = func(ctx context.Context, st *op.ObjectState, set map[string][]byte, rm []string) error {
					if stub.SetObjectAttrsCallCount() == 1 {
						cur, err := f.store.StatObject(ctx, f.rec, meta.ObjKey{Name: "small"})
						Expect(err).NotTo(HaveOccurred())
						Expect(f.store.SetObjectAttrs(ctx, cur, map[string][]byte{meta.AttrACL: encodedPolicy(concurrent)}, nil)).To(Succeed())
						return op.ErrConcurrentModification
					}
					return f.store.SetObjectAttrs(ctx, st, set, rm)
				}
			}
			bobsRequest := func() *op.Request {
				r := f.req(http.MethodPut, "plain", "small")
				r.Identity = f.bob
				return r
			}
			wanted := func() acl.Policy {
				p := bobCanWriteACP()
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermRead})
				return p
			}
			It("refuses the retry once the write it lost to revoked the requester's WRITE_ACP, leaving the revoking ACL", func(ctx SpecContext) {
				revoking := acl.DefaultPolicy(f.alice.Owner, "Alice")
				race(ctx, revoking)
				o := &op.PutObjectACL{Build: func(acl.Policy) (acl.Policy, error) { return wanted(), nil }}
				Expect(op.Run(ctx, o, bobsRequest())).To(MatchError(op.ErrAccessDenied))
				Expect(stub.SetObjectAttrsCallCount()).To(Equal(1), "the retry writes nothing")
				Expect(f.stat(ctx, "small").Attrs).To(HaveKeyWithValue(meta.AttrACL, encodedPolicy(revoking)))
			})
			It("lets an admin through a retry's refusal, as Run lets one through the first check", func(ctx SpecContext) {
				race(ctx, acl.DefaultPolicy(f.alice.Owner, "Alice"))
				r := bobsRequest()
				r.Identity.Admin = true
				o := &op.PutObjectACL{Build: func(acl.Policy) (acl.Policy, error) { return wanted(), nil }}
				Expect(op.Run(ctx, o, r)).To(Succeed())
				Expect(stub.SetObjectAttrsCallCount()).To(Equal(2))
			})
			DescribeTable("lets an admin through no retry refusal Run would not override, and a non-admin through none",
				func(ctx SpecContext, admin bool, refusal error) {
					fake := &opfakes.FakeAuthorizer{}
					fake.VerifyObjectReturnsOnCall(1, refusal)
					f.env.Authz = fake
					stub.SetObjectAttrsReturnsOnCall(0, op.ErrConcurrentModification)
					r := f.req(http.MethodPut, "plain", "small")
					r.Identity.Admin = admin
					o := &op.PutObjectACL{Build: func(p acl.Policy) (acl.Policy, error) { return p, nil }}
					Expect(op.Run(ctx, o, r)).To(MatchError(refusal))
					Expect(fake.VerifyObjectCallCount()).To(Equal(2), "Run's check and the retry's")
					Expect(stub.SetObjectAttrsCallCount()).To(Equal(1), "the retry writes nothing")
				},
				Entry("a non-admin under an unmarked access denial", false, op.ErrAccessDenied),
				Entry("an admin under a refusal marked BeforeVerify", true, op.BeforeVerify(op.ErrAccessDenied)),
				Entry("an admin under a refusal that is no access denial, MFA's", true, op.ErrMFARequired),
				Entry("an admin under an error that is no refusal", true, op.ErrInternalError),
			)
			It("stores the ACL on a retry whose requester still holds WRITE_ACP", func(ctx SpecContext) {
				race(ctx, bobCanWriteACP())
				o := &op.PutObjectACL{Build: func(acl.Policy) (acl.Policy, error) { return wanted(), nil }}
				Expect(op.Run(ctx, o, bobsRequest())).To(Succeed())
				Expect(stub.SetObjectAttrsCallCount()).To(Equal(2))
				Expect(f.stat(ctx, "small").Attrs).To(HaveKeyWithValue(meta.AttrACL, encodedPolicy(wanted())))
			})
		})
		It("answers NoSuchKey when the object is gone by the retry", func(ctx SpecContext) {
			stub.StatObjectReturnsOnCall(1, &op.ObjectState{}, nil)
			stub.SetObjectAttrsReturnsOnCall(0, op.ErrConcurrentModification)
			Expect(op.Run(ctx, &op.PutObjectACL{Build: keep}, f.req(http.MethodPut, "plain", "small"))).To(MatchError(op.ErrNoSuchKey))
			Expect(stub.SetObjectAttrsCallCount()).To(Equal(1))
		})
	})
	It("answers a missing object with NoSuchKey without building a policy", func(ctx SpecContext) {
		built := false
		o := &op.PutObjectACL{Build: func(p acl.Policy) (acl.Policy, error) {
			built = true
			return p, nil
		}}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "missing"))).To(MatchError(op.ErrNoSuchKey))
		Expect(built).To(BeFalse())
	})
})

var _ = Describe("PutObjectTagging", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	DescribeTable("is radosgw's put_obj_tags, a write of the action the instance selects",
		func(ctx SpecContext, instance string, want policy.Action, wantErr error) {
			fake := &opfakes.FakeAuthorizer{}
			f.env.Authz = fake
			o := &op.PutObjectTagging{}
			r := f.req(http.MethodPut, "plain", "small")
			r.Object.Instance = instance
			err := op.Run(ctx, o, r)
			if wantErr == nil {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(wantErr))
			}
			Expect(o.Name()).To(Equal("put_obj_tags"))
			Expect(o.OpMask()).To(Equal(op.OpTypeWrite))
			Expect(o.Action()).To(Equal(want))
			_, _, a, perm := fake.VerifyObjectArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
		},
		Entry("no instance", "", policy.S3PutObjectTagging, nil),
		Entry("the null instance, the plain object on a bucket whose versioning was never enabled", "null", policy.S3PutObjectVersionTagging, nil),
		Entry("a version, which is not served yet", "v1", policy.S3PutObjectVersionTagging, op.ErrNotImplemented),
	)
	It("stores the tag set and keeps every other attr", func(ctx SpecContext) {
		before := f.stat(ctx, "small")
		set := tags.Set{}
		Expect(set.Add("k", "v", tags.MaxObjectTags)).To(Succeed())
		Expect(op.Run(ctx, &op.PutObjectTagging{Set: set}, f.req(http.MethodPut, "plain", "small"))).To(Succeed())
		after := f.stat(ctx, "small")
		Expect(after.Attrs).To(HaveKeyWithValue(tags.Attr, encodedTags(set)))
		for k, v := range before.Attrs {
			Expect(after.Attrs).To(HaveKeyWithValue(k, v))
		}
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("stores an empty tag set, which get_params encodes whole", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.PutObjectTagging{}, f.req(http.MethodPut, "plain", "small"))).To(Succeed())
		Expect(f.stat(ctx, "small").Attrs).To(HaveKeyWithValue(tags.Attr, encodedTags(tags.Set{})))
	})
	It("passes the object's whole attr set with the tags replaced to the store", func(ctx SpecContext) {
		stub := &opfakes.FakeObjectStore{}
		st := &op.ObjectState{Exists: true, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL, tags.Attr: []byte("old")}}
		stub.StatObjectReturns(st, nil)
		f.env.Objects = stub
		Expect(op.Run(ctx, &op.PutObjectTagging{}, f.req(http.MethodPut, "plain", "small"))).To(Succeed())
		_, gotSt, set, rm := stub.SetObjectAttrsArgsForCall(0)
		Expect(gotSt).To(BeIdenticalTo(st))
		Expect(set).To(Equal(map[string][]byte{meta.AttrACL: f.aliceACL, tags.Attr: encodedTags(tags.Set{})}))
		Expect(rm).To(BeEmpty())
	})
	It("answers a lost race with 409 OperationAborted", func(ctx SpecContext) {
		stub := &opfakes.FakeObjectStore{}
		stub.StatObjectReturns(&op.ObjectState{Exists: true, Attrs: map[string][]byte{}}, nil)
		stub.SetObjectAttrsReturns(op.ErrConcurrentModification)
		f.env.Objects = stub
		Expect(op.Run(ctx, &op.PutObjectTagging{}, f.req(http.MethodPut, "plain", "small"))).To(MatchError(op.ErrTagConflict))
	})
	It("answers a missing object with NoSuchKey", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.PutObjectTagging{}, f.req(http.MethodPut, "plain", "missing"))).To(MatchError(op.ErrNoSuchKey))
	})
	Describe("Params", func() {
		set := func() tags.Set {
			s := tags.Set{}
			Expect(s.Add("p", "q", tags.MaxObjectTags)).To(Succeed())
			return s
		}
		It("runs once the requester is authorized and stores the set it parsed", func(ctx SpecContext) {
			fake := &opfakes.FakeAuthorizer{}
			f.env.Authz = fake
			o := &op.PutObjectTagging{Params: func(_ context.Context, o *op.PutObjectTagging) error {
				Expect(fake.VerifyObjectCallCount()).To(Equal(1), "verify_permission runs before execute's get_params")
				o.Set = set()
				return nil
			}}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "small"))).To(Succeed())
			Expect(f.stat(ctx, "small").Attrs).To(HaveKeyWithValue(tags.Attr, encodedTags(set())))
		})
		It("is not reached for a refused requester, a missing object or a version", func(ctx SpecContext) {
			called := false
			params := func(context.Context, *op.PutObjectTagging) error { called = true; return nil }
			refused := f.req(http.MethodPut, "plain", "small")
			refused.Identity = f.bob
			Expect(op.Run(ctx, &op.PutObjectTagging{Params: params}, refused)).To(MatchError(op.ErrAccessDenied))
			Expect(op.Run(ctx, &op.PutObjectTagging{Params: params}, f.req(http.MethodPut, "plain", "missing"))).To(MatchError(op.ErrNoSuchKey))
			versioned := f.req(http.MethodPut, "plain", "small")
			versioned.Object.Instance = "v1"
			Expect(op.Run(ctx, &op.PutObjectTagging{Params: params}, versioned)).To(MatchError(op.ErrNotImplemented))
			Expect(called).To(BeFalse())
		})
		It("returns its error and stores nothing", func(ctx SpecContext) {
			parseErr := errors.New("bad body")
			o := &op.PutObjectTagging{Params: func(context.Context, *op.PutObjectTagging) error { return parseErr }}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "small"))).To(MatchError(parseErr))
			Expect(f.stat(ctx, "small").Attrs).NotTo(HaveKey(tags.Attr))
		})
	})
})

var _ = Describe("DeleteObjectTagging", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	DescribeTable("is radosgw's delete_obj_tags, a delete of the action the instance selects",
		func(ctx SpecContext, instance string, want policy.Action, wantErr error) {
			fake := &opfakes.FakeAuthorizer{}
			f.env.Authz = fake
			o := &op.DeleteObjectTagging{}
			r := f.req(http.MethodDelete, "plain", "small")
			r.Object.Instance = instance
			err := op.Run(ctx, o, r)
			if wantErr == nil {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(wantErr))
			}
			Expect(o.Name()).To(Equal("delete_obj_tags"))
			Expect(o.OpMask()).To(Equal(op.OpTypeDelete))
			Expect(o.Action()).To(Equal(want))
			_, _, a, perm := fake.VerifyObjectArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
		},
		Entry("no instance", "", policy.S3DeleteObjectTagging, nil),
		Entry("the null instance, the plain object on a bucket whose versioning was never enabled", "null", policy.S3DeleteObjectVersionTagging, nil),
		Entry("a version, which is not served yet", "v1", policy.S3DeleteObjectVersionTagging, op.ErrNotImplemented),
	)
	It("removes the tag attr and logs no usage", func(ctx SpecContext) {
		set := tags.Set{}
		Expect(set.Add("k", "v", tags.MaxObjectTags)).To(Succeed())
		st := f.stat(ctx, "small")
		Expect(f.store.SetObjectAttrs(ctx, st, map[string][]byte{tags.Attr: encodedTags(set)}, nil)).To(Succeed())
		Expect(op.Run(ctx, &op.DeleteObjectTagging{}, f.req(http.MethodDelete, "plain", "small"))).To(Succeed())
		Expect(f.stat(ctx, "small").Attrs).NotTo(HaveKey(tags.Attr))
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	DescribeTable("removes the tags from the state Init read, whose mtime the store advances, on both releases",
		func(ctx SpecContext, release denc.Release) {
			f = newWriteFixture(ctx, release)
			stub := &opfakes.FakeObjectStore{}
			st := &op.ObjectState{Exists: true, Mtime: smallMtime(), Attrs: map[string][]byte{tags.Attr: []byte("t")}}
			stub.StatObjectReturns(st, nil)
			f.env.Objects = stub
			Expect(op.Run(ctx, &op.DeleteObjectTagging{}, f.req(http.MethodDelete, "plain", "small"))).To(Succeed())
			_, gotSt, set, rm := stub.SetObjectAttrsArgsForCall(0)
			Expect(gotSt).To(BeIdenticalTo(st))
			Expect(gotSt.Mtime).To(Equal(smallMtime()), "not Squid's zero mtime")
			Expect(set).To(BeEmpty())
			Expect(rm).To(Equal([]string{tags.Attr}))
		},
		Entry("Squid", denc.Squid),
		Entry("Tentacle", denc.Tentacle),
	)
	It("passes a lost race on as 409 ConcurrentModification", func(ctx SpecContext) {
		stub := &opfakes.FakeObjectStore{}
		stub.StatObjectReturns(&op.ObjectState{Exists: true, Mtime: time.Unix(1, 0)}, nil)
		stub.SetObjectAttrsReturns(op.ErrConcurrentModification)
		f.env.Objects = stub
		Expect(op.Run(ctx, &op.DeleteObjectTagging{}, f.req(http.MethodDelete, "plain", "small"))).To(MatchError(op.ErrConcurrentModification))
	})
	It("answers a missing object with NoSuchKey", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.DeleteObjectTagging{}, f.req(http.MethodDelete, "plain", "missing"))).To(MatchError(op.ErrNoSuchKey))
	})
})
