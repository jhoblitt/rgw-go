package op_test

import (
	"context"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("ListParts", func() {
	var (
		f  *writeFixture
		id string
	)
	BeforeEach(func(ctx SpecContext) {
		f = newMPFixture(ctx, denc.Squid)
		id = f.initUpload(ctx, "k")
	})
	// metaKey is the meta object of key's upload uploadID.
	metaKey := func(key, uploadID string) meta.ObjKey {
		return meta.ObjKey{Name: meta.MultipartMetaName(key, uploadID), NS: meta.NSMultipart}
	}
	// stubbed makes the multipart store a fake whose GetUpload returns up.
	stubbed := func(up *op.Upload) *opfakes.FakeMultipartStore {
		stub := &opfakes.FakeMultipartStore{}
		stub.GetUploadReturns(up, nil)
		f.env.Multipart = stub
		return stub
	}

	It("is radosgw's list_multipart, a read of s3:ListMultipartUploadParts", func() {
		o := &op.ListParts{}
		Expect(o.Name()).To(Equal("list_multipart"))
		Expect(o.Action()).To(Equal(policy.S3ListMultipartUploadParts))
		Expect(o.OpMask()).To(Equal(op.OpTypeRead))
	})
	It("lists a page of the parts past the marker, with the upload's owner and storage class", func(ctx SpecContext) {
		for n := range 3 {
			f.uploadPart(ctx, "k", id, n+1, []byte("x"))
		}
		o := &op.ListParts{UploadID: id, Marker: 1, MaxParts: 1}
		Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", "k"))).To(Succeed())
		Expect(o.Result.Parts).To(ConsistOf(HaveField("Number", 2)))
		Expect([]any{o.Result.NextMarker, o.Result.Truncated}).To(Equal([]any{2, true}))
		Expect(o.Owner).To(Equal(acl.Owner{ID: "alice", DisplayName: "Alice"}))
		Expect(o.StorageClass).To(Equal(meta.StorageClassStandard))
		Expect(o.Upload.ID).To(Equal(id))
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	DescribeTable("reports the upload's checksum algorithm and type, and each part's checksum, on Tentacle, and none on Squid",
		func(ctx SpecContext, rel denc.Release, algorithm, wantType string) {
			f = newMPFixture(ctx, rel)
			init := &op.InitMultipart{ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"), ChecksumField: checksumFields("x-amz-checksum-algorithm", algorithm)}
			Expect(op.Run(ctx, init, f.req(http.MethodPost, "plain", "k"))).To(Succeed())
			f.uploadPart(ctx, "k", init.UploadID, 1, []byte("x"))
			o := &op.ListParts{UploadID: init.UploadID, MaxParts: 10}
			Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", "k"))).To(Succeed())
			t, _ := cksum.ParseName(algorithm)
			if rel < denc.Tentacle {
				t = cksum.None
			}
			Expect([]any{o.ChecksumAlgorithm, o.ChecksumType}).To(Equal([]any{t, wantType}))
			Expect(o.Result.Parts).To(HaveExactElements(HaveField("Checksum", WithTransform(func(c *cksum.Cksum) cksum.Type {
				if c == nil {
					return cksum.None
				}
				return c.Type
			}, Equal(t)))))
		},
		Entry("a CRC32C upload on Tentacle, FULL_OBJECT", denc.Tentacle, "CRC32C", cksum.FullObjectType),
		Entry("a SHA256 upload on Tentacle, COMPOSITE", denc.Tentacle, "SHA256", cksum.CompositeType),
		Entry("a CRC32C upload on Squid, whose upload keeps no type", denc.Squid, "CRC32C", ""),
	)
	DescribeTable("types the upload's checksum by how its completion combines the parts",
		func(ctx SpecContext, uc op.UploadChecksum, want string) {
			stubbed(&op.Upload{ID: id, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL}, Checksum: uc})
			o := &op.ListParts{UploadID: id, MaxParts: 10}
			Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", "k"))).To(Succeed())
			Expect([]any{o.ChecksumAlgorithm, o.ChecksumType}).To(Equal([]any{uc.Type, want}))
		},
		Entry("a CRC created FULL_OBJECT", op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagFullObject}, cksum.FullObjectType),
		Entry("a CRC created COMPOSITE", op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagComposite}, cksum.CompositeType),
		Entry("a CRC created with a type header naming neither, flags 0", op.UploadChecksum{Type: cksum.CRC64NVME}, cksum.FullObjectType),
		Entry("a digest", op.UploadChecksum{Type: cksum.SHA1, Flags: cksum.FlagComposite}, cksum.CompositeType),
		Entry("xxhash3, an AWS type under its AWS name", op.UploadChecksum{Type: cksum.XXH3, Flags: cksum.FlagComposite}, cksum.CompositeType),
	)
	It("reports no algorithm, and no part checksum, of a type AWS does not define, as radosgw renders only AWS types", func(ctx SpecContext) {
		stub := stubbed(&op.Upload{ID: id, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL}, Checksum: op.UploadChecksum{Type: cksum.BLAKE3, Flags: cksum.FlagComposite}})
		blake := &cksum.Cksum{Type: cksum.BLAKE3, Digest: make([]byte, 32), Flags: cksum.FlagComposite}
		crc := &cksum.Cksum{Type: cksum.CRC32, Digest: make([]byte, 4)}
		stub.ListPartsReturns(op.ListPartsResult{Parts: []op.Part{{Number: 1, Checksum: blake}, {Number: 2, Checksum: crc}, {Number: 3}}}, nil)
		o := &op.ListParts{UploadID: id, MaxParts: 10}
		Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", "k"))).To(Succeed())
		Expect([]any{o.ChecksumAlgorithm, o.ChecksumType}).To(Equal([]any{cksum.None, ""}))
		Expect(o.Result.Parts).To(HaveExactElements(HaveField("Checksum", BeNil()), HaveField("Checksum", Equal(crc)), HaveField("Checksum", BeNil())))
	})
	It("reports the storage class of the upload's placement", func(ctx SpecContext) {
		stubbed(&op.Upload{ID: id, Placement: meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL}})
		o := &op.ListParts{UploadID: id, MaxParts: 10}
		Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", "k"))).To(Succeed())
		Expect(o.StorageClass).To(Equal("COLD"))
	})
	It("hands the store the upload, the marker and the page size", func(ctx SpecContext) {
		up := &op.Upload{ID: id, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL}}
		stub := stubbed(up)
		Expect(op.Run(ctx, &op.ListParts{UploadID: id, Marker: 4, MaxParts: 9}, f.req(http.MethodGet, "plain", "k"))).To(Succeed())
		_, rec, key, uploadID := stub.GetUploadArgsForCall(0)
		Expect([]any{rec.Info.Bucket.Name, key, uploadID}).To(Equal([]any{"plain", meta.ObjKey{Name: "k"}, id}))
		_, got, marker, maxParts := stub.ListPartsArgsForCall(0)
		Expect([]any{got, marker, maxParts}).To(Equal([]any{up, 4, 9}))
	})
	It("authorizes s3:ListMultipartUploadParts on the upload's meta object, its attrs as the object's", func(ctx SpecContext) {
		fake := &opfakes.FakeAuthorizer{}
		var seen *op.ObjectState
		fake.VerifyObjectCalls(func(_ context.Context, r *op.Request, _ policy.Action, _ acl.Permission) error {
			seen = r.ObjState
			return nil
		})
		f.env.Authz = fake
		Expect(op.Run(ctx, &op.ListParts{UploadID: id, MaxParts: 10}, f.req(http.MethodGet, "plain", "k"))).To(Succeed())
		Expect(fake.VerifyObjectCallCount()).To(Equal(1))
		_, _, a, perm := fake.VerifyObjectArgsForCall(0)
		Expect([]any{a, perm}).To(Equal([]any{policy.S3ListMultipartUploadParts, acl.PermFor(policy.S3ListMultipartUploadParts)}))
		up, err := f.upload(ctx, "k", id)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{seen.Exists, seen.Key, seen.Bucket.Info.Bucket.Name}).To(Equal([]any{true, metaKey("k", id), "plain"}))
		Expect(seen.Attrs).To(Equal(up.Attrs))
	})
	It("authorizes against an upload's missing meta object, whose rule decides the answer, not NoSuchUpload", func(ctx SpecContext) {
		fake := &opfakes.FakeAuthorizer{}
		fake.VerifyObjectReturns(op.ErrNoSuchKey)
		f.env.Authz = fake
		stub := &opfakes.FakeMultipartStore{}
		stub.GetUploadReturns(nil, op.ErrNoSuchUpload)
		f.env.Multipart = stub
		r := f.req(http.MethodGet, "plain", "k")
		Expect(op.Run(ctx, &op.ListParts{UploadID: "2~nope", MaxParts: 10}, r)).To(MatchError(op.ErrNoSuchKey))
		Expect([]any{r.ObjState.Exists, r.ObjState.Key}).To(Equal([]any{false, metaKey("k", "2~nope")}))
		Expect(stub.ListPartsCallCount()).To(BeZero())
	})
	It("answers NoSuchUpload for a missing upload when the missing-object rule lets it through", func(ctx SpecContext) {
		f.env.Authz = &opfakes.FakeAuthorizer{}
		Expect(op.Run(ctx, &op.ListParts{UploadID: "2~nope", MaxParts: 10}, f.req(http.MethodGet, "plain", "k"))).To(MatchError(op.ErrNoSuchUpload))
	})
	It("returns another error reading the upload as it is", func(ctx SpecContext) {
		stub := &opfakes.FakeMultipartStore{}
		stub.GetUploadReturns(nil, op.ErrServiceUnavailable)
		f.env.Multipart = stub
		Expect(op.Run(ctx, &op.ListParts{UploadID: id, MaxParts: 10}, f.req(http.MethodGet, "plain", "k"))).To(MatchError(op.ErrServiceUnavailable))
	})
	Describe("an empty upload id", func() {
		It("authorizes against the object's own state, then answers NoSuchUpload", func(ctx SpecContext) {
			f.put(ctx, "k", []byte("x"))
			fake := &opfakes.FakeAuthorizer{}
			f.env.Authz = fake
			stub := &opfakes.FakeMultipartStore{}
			f.env.Multipart = stub
			r := f.req(http.MethodGet, "plain", "k")
			Expect(op.Run(ctx, &op.ListParts{MaxParts: 10}, r)).To(MatchError(op.ErrNoSuchUpload))
			Expect([]any{r.ObjState.Exists, r.ObjState.Key}).To(Equal([]any{true, meta.ObjKey{Name: "k"}}))
			Expect(fake.VerifyObjectCallCount()).To(Equal(1))
			Expect(stub.Invocations()).To(BeEmpty())
		})
		It("takes a missing object's answer from read_obj_policy's rule", func(ctx SpecContext) {
			fake := &opfakes.FakeAuthorizer{}
			fake.VerifyObjectReturns(op.ErrAccessDenied)
			f.env.Authz = fake
			r := f.req(http.MethodGet, "plain", "k")
			Expect(op.Run(ctx, &op.ListParts{MaxParts: 10}, r)).To(MatchError(op.ErrAccessDenied))
			Expect(r.ObjState.Exists).To(BeFalse())
		})
	})
	It("reports no owner for an upload whose meta object has no ACL", func(ctx SpecContext) {
		stubbed(&op.Upload{ID: id})
		f.env.Authz = &opfakes.FakeAuthorizer{}
		o := &op.ListParts{UploadID: id, MaxParts: 10}
		Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", "k"))).To(Succeed())
		Expect(o.Owner).To(Equal(acl.Owner{}))
	})
	It("answers 500 UnknownError, decode_policy's EIO, for a meta object ACL that does not decode, listing nothing", func(ctx SpecContext) {
		stub := stubbed(&op.Upload{ID: id, Attrs: map[string][]byte{meta.AttrACL: {0xff}}})
		f.env.Authz = &opfakes.FakeAuthorizer{}
		Expect(op.Run(ctx, &op.ListParts{UploadID: id, MaxParts: 10}, f.req(http.MethodGet, "plain", "k"))).To(MatchError(op.ErrUnknown))
		Expect(stub.ListPartsCallCount()).To(BeZero())
	})
	It("lists nothing for a requester refused permission", func(ctx SpecContext) {
		r := f.req(http.MethodGet, "plain", "k")
		r.Identity = f.bob
		Expect(op.Run(ctx, &op.ListParts{UploadID: id, MaxParts: 10}, r)).To(MatchError(op.ErrAccessDenied))
	})
	It("refuses a bucket that does not exist before checking permissions", func(ctx SpecContext) {
		fake := &opfakes.FakeAuthorizer{}
		f.env.Authz = fake
		Expect(op.Run(ctx, &op.ListParts{UploadID: id}, f.req(http.MethodGet, "nope", "k"))).To(MatchError(op.ErrNoSuchBucket))
		Expect(fake.Invocations()).To(BeEmpty())
	})

	Describe("with radosgw's ACL evaluation, when no policy decides", func() {
		var bob op.Identity
		BeforeEach(func() {
			f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
			u := meta.NewUserInfo()
			u.UserID, u.DisplayName, u.Type = meta.UserID{ID: "bob"}, "Bob", meta.IdentityRGW
			bob = op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, AccessKey: "bob"}
		})
		readBy := func(who string) acl.Policy {
			p := acl.DefaultPolicy(f.alice.Owner, "Alice")
			p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: who, Name: who, Permission: acl.PermRead})
			return p
		}
		// initWith starts alice's upload of key k under policy p.
		initWith := func(ctx context.Context, p acl.Policy) string {
			GinkgoHelper()
			o := &op.InitMultipart{ACL: p}
			Expect(op.Run(ctx, o, f.req(http.MethodPost, "plain", "k"))).To(Succeed())
			return o.UploadID
		}
		bobLists := func(ctx context.Context, uploadID string) (*op.ListParts, error) {
			r := f.req(http.MethodGet, "plain", "k")
			r.Identity = bob
			o := &op.ListParts{UploadID: uploadID, MaxParts: 10}
			return o, op.Run(ctx, o, r)
		}

		It("lets a grantee of the upload's own ACL list its parts", func(ctx SpecContext) {
			up := initWith(ctx, readBy("bob"))
			f.uploadPart(ctx, "k", up, 1, []byte("x"))
			o, err := bobLists(ctx, up)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Result.Parts).To(HaveLen(1))
		})
		It("refuses a grantee of the bucket alone, as the meta object's ACL decides, not the bucket's", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(readBy("bob"))})
			_, err := bobLists(ctx, id)
			Expect(err).To(MatchError(op.ErrAccessDenied))
		})
		It("refuses a grantee of the object of the same key, whose ACL is not the upload's", func(ctx SpecContext) {
			f.putAttrs(ctx, "k", "x", map[string][]byte{meta.AttrACL: encodedPolicy(readBy("bob"))})
			_, err := bobLists(ctx, id)
			Expect(err).To(MatchError(op.ErrAccessDenied))
		})
		It("answers a requester who may not list the bucket AccessDenied whether or not the upload exists", func(ctx SpecContext) {
			_, existing := bobLists(ctx, id)
			_, missing := bobLists(ctx, "2~nope")
			Expect(existing).To(MatchError(op.ErrAccessDenied))
			Expect(missing).To(MatchError(op.ErrAccessDenied))
			Expect(messageOf(missing)).To(Equal(messageOf(existing)))
		})
		It("answers a requester who may list the bucket NoSuchKey for a missing upload, as read_obj_policy's ENOENT", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(readBy("bob"))})
			_, err := bobLists(ctx, "2~nope")
			Expect(err).To(MatchError(op.ErrNoSuchKey))
		})
		It("gives a meta object without an ACL the bucket owner's default policy, which refuses others", func(ctx SpecContext) {
			stub := stubbed(&op.Upload{ID: id, Attrs: map[string][]byte{}})
			_, err := bobLists(ctx, id)
			Expect(err).To(MatchError(op.ErrAccessDenied))
			Expect(stub.ListPartsCallCount()).To(BeZero())
			Expect(op.Run(ctx, &op.ListParts{UploadID: id, MaxParts: 10}, f.req(http.MethodGet, "plain", "k"))).To(Succeed(), "the bucket owner")
		})
		It("owns a meta object without an ACL by the bucket owner, not the upload's initiator", func(ctx SpecContext) {
			stub := stubbed(&op.Upload{ID: id, Owner: bob.Owner, OwnerName: "Bob", Attrs: map[string][]byte{}})
			stub.ListPartsReturns(op.ListPartsResult{}, nil)
			_, err := bobLists(ctx, id)
			Expect(err).To(MatchError(op.ErrAccessDenied), "the initiator")
			Expect(op.Run(ctx, &op.ListParts{UploadID: id, MaxParts: 10}, f.req(http.MethodGet, "plain", "k"))).To(Succeed(), "the bucket owner")
			Expect(stub.ListPartsCallCount()).To(Equal(1))
		})
		It("refuses everyone a meta object whose ACL does not decode, before the op mask, listing nothing", func(ctx SpecContext) {
			stub := stubbed(&op.Upload{ID: id, Attrs: map[string][]byte{meta.AttrACL: {0xff}}})
			_, err := bobLists(ctx, id)
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(op.IsBeforeVerify(err)).To(BeTrue())
			Expect(op.Run(ctx, &op.ListParts{UploadID: id, MaxParts: 10}, f.req(http.MethodGet, "plain", "k"))).To(MatchError(op.ErrUnknown))
			Expect(stub.ListPartsCallCount()).To(BeZero())
		})
		It("lists no upload's parts for an empty upload id, whatever the object's ACL grants", func(ctx SpecContext) {
			f.putAttrs(ctx, "k", "x", map[string][]byte{meta.AttrACL: encodedPolicy(readBy("bob"))})
			f.uploadPart(ctx, "k", id, 1, []byte("x"))
			o, err := bobLists(ctx, "")
			Expect(err).To(MatchError(op.ErrNoSuchUpload))
			Expect(o.Result.Parts).To(BeEmpty())
		})
	})
})
