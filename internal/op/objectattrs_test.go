package op_test

import (
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
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
		func(ctx SpecContext, instance string, want policy.Action) {
			fake := &opfakes.FakeAuthorizer{}
			f.env.Authz = fake
			o := &op.PutObjectACL{Build: func(p acl.Policy) (acl.Policy, error) { return p, nil }}
			r := f.req(http.MethodPut, "plain", "small")
			r.Object.Instance = instance
			err := op.Run(ctx, o, r)
			if instance == "" {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(op.ErrNotImplemented), "a version is not served yet")
			}
			Expect(o.Name()).To(Equal("put_acls"))
			Expect(o.OpMask()).To(Equal(op.OpTypeWrite))
			Expect(o.Action()).To(Equal(want))
			_, _, a, perm := fake.VerifyObjectArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
		},
		Entry("no instance", "", policy.S3PutObjectAcl),
		Entry("the null instance", "null", policy.S3PutObjectVersionAcl),
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
	It("answers a lost race with success, as ACLs are immutable", func(ctx SpecContext) {
		stub := &opfakes.FakeObjectStore{}
		stub.StatObjectReturns(&op.ObjectState{Exists: true, Attrs: map[string][]byte{}}, nil)
		stub.SetObjectAttrsReturns(op.ErrConcurrentModification)
		f.env.Objects = stub
		o := &op.PutObjectACL{Build: func(p acl.Policy) (acl.Policy, error) { return p, nil }}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "small"))).To(Succeed())
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
		func(ctx SpecContext, instance string, want policy.Action) {
			fake := &opfakes.FakeAuthorizer{}
			f.env.Authz = fake
			o := &op.PutObjectTagging{}
			r := f.req(http.MethodPut, "plain", "small")
			r.Object.Instance = instance
			err := op.Run(ctx, o, r)
			if instance == "" {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(op.ErrNotImplemented), "a version is not served yet")
			}
			Expect(o.Name()).To(Equal("put_obj_tags"))
			Expect(o.OpMask()).To(Equal(op.OpTypeWrite))
			Expect(o.Action()).To(Equal(want))
			_, _, a, perm := fake.VerifyObjectArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
		},
		Entry("no instance", "", policy.S3PutObjectTagging),
		Entry("the null instance", "null", policy.S3PutObjectVersionTagging),
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
})

var _ = Describe("DeleteObjectTagging", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	DescribeTable("is radosgw's delete_obj_tags, a delete of the action the instance selects",
		func(ctx SpecContext, instance string, want policy.Action) {
			fake := &opfakes.FakeAuthorizer{}
			f.env.Authz = fake
			o := &op.DeleteObjectTagging{}
			r := f.req(http.MethodDelete, "plain", "small")
			r.Object.Instance = instance
			err := op.Run(ctx, o, r)
			if instance == "" {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(op.ErrNotImplemented), "a version is not served yet")
			}
			Expect(o.Name()).To(Equal("delete_obj_tags"))
			Expect(o.OpMask()).To(Equal(op.OpTypeDelete))
			Expect(o.Action()).To(Equal(want))
			_, _, a, perm := fake.VerifyObjectArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
		},
		Entry("no instance", "", policy.S3DeleteObjectTagging),
		Entry("the null instance", "null", policy.S3DeleteObjectVersionTagging),
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
