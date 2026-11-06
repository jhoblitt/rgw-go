package op_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// msgForbiddenByLock is the s->err.message DeleteObject sets when object lock
// refuses it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5213-L5219 at v19.2.7).
const msgForbiddenByLock = "forbidden by object lock"

// lockedDeleteAttempts is the delete under object lock's first attempt and
// its three re-checks.
const lockedDeleteAttempts = 4

// The retain-until dates the delete matrix tries, by how their whole second
// relates to lockNow's, which falls on a whole second.
var deleteUntilByRel = map[string]time.Time{
	"a second in the past": lockNow.Add(-time.Second),
	"now's own second":     lockNow.Add(400 * time.Millisecond),
	"the next second":      lockNow.Add(time.Second),
}

// deleteLockMatrix is L1's VerifyDelete matrix as both deletes meet it:
// every stored mode, retain-until second, bypass header, bypass permission
// and legal hold, for DeleteObject and for a DeleteObjects key, with whether
// the version is kept.
func deleteLockMatrix() []TableEntry {
	var entries []TableEntry
	holds := []string{"", "OFF", "ON"}
	for _, multi := range []bool{false, true} {
		via := "DeleteObject"
		if multi {
			via = "DeleteObjects"
		}
		for _, mode := range []string{"GOVERNANCE", "COMPLIANCE"} {
			for rel, until := range deleteUntilByRel {
				for _, header := range []bool{false, true} {
					for _, allowed := range []bool{false, true} {
						for _, hold := range holds {
							bypassed := mode == "GOVERNANCE" && header && allowed
							locked := rel == "the next second" && !bypassed || hold == "ON"
							name := fmt.Sprintf("%s, mode %s until %s, header %t, permission %t, legal hold %q", via, mode, rel, header, allowed, hold)
							entries = append(entries, Entry(name, multi, mode, until, header, allowed, hold, locked))
						}
					}
				}
			}
		}
		for _, hold := range holds {
			for _, header := range []bool{false, true} {
				entries = append(entries, Entry(fmt.Sprintf("%s, no retention, header %t, legal hold %q", via, header, hold),
					multi, "", time.Time{}, header, true, hold, hold == "ON"))
			}
		}
	}
	return entries
}

var _ = Describe("the object-lock check of DeleteObject and DeleteObjects", func() {
	var (
		f *writeFixture
		m *multiDelete
	)
	BeforeEach(func(ctx SpecContext) {
		f = newWriteFixture(ctx, denc.Squid)
		f.env.Now = func() time.Time { return lockNow }
		m = &multiDelete{}
	})
	small := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "small", Instance: instance} }

	// lock gives key's object in plain a retention of mode until, when mode
	// is set, and a legal hold of hold, when it is set.
	lock := func(ctx context.Context, key, mode string, until time.Time, hold string) {
		GinkgoHelper()
		set := map[string][]byte{}
		if mode != "" {
			set[meta.AttrObjectRetention] = retentionBytes(mode, until)
		}
		if hold != "" {
			set[meta.AttrObjectLegalHold] = legalHoldBytes(hold)
		}
		Expect(f.store.SetObjectAttrs(ctx, f.stat(ctx, key), set, nil)).To(Succeed())
	}
	// del is DeleteObject of key as id, with the bypass header when header is
	// set.
	del := func(ctx context.Context, id op.Identity, key meta.ObjKey, header bool) (*op.DeleteObject, error) {
		r := f.req(http.MethodDelete, "plain", key.Name)
		r.Object, r.Identity = key, id
		o := &op.DeleteObject{BypassHeader: header}
		return o, op.Run(ctx, o, r)
	}
	// multi is DeleteObjects of keys as id, with the bypass header when
	// header is set, and each key's error by its name.
	multi := func(ctx context.Context, id op.Identity, header bool, keys ...meta.ObjKey) map[string]error {
		GinkgoHelper()
		m = &multiDelete{}
		es := make([]op.DeleteObjectsEntry, len(keys))
		for i, k := range keys {
			es[i] = op.DeleteObjectsEntry{Key: k}
		}
		o := m.op(es, nil)
		o.BypassHeader = header
		r := f.multiReq("x")
		r.Identity = id
		Expect(op.Run(ctx, o, r)).To(Succeed())
		Expect(m.results).To(HaveLen(len(keys)))
		return errByKey(m.results)
	}
	expectForbidden := func(err error) {
		GinkgoHelper()
		Expect(err).To(MatchError(op.ErrAccessDenied))
		Expect(op.AsError(err).Status).To(Equal(http.StatusForbidden))
		Expect(op.AsError(err).Message).To(Equal(msgForbiddenByLock))
	}
	// bypassFake is an authorizer that allows everything but the bypass, which
	// it allows when allowed says so, counting its calls.
	bypassFake := func(allowed func(r *op.Request) bool) *opfakes.FakeAuthorizer {
		fake := &opfakes.FakeAuthorizer{}
		fake.VerifyBucketOwnerOrPolicyStub = func(_ context.Context, r *op.Request, a policy.Action, _ acl.Permission) error {
			if a != policy.S3BypassGovernanceRetention || !allowed(r) {
				return op.ErrAccessDenied
			}
			return nil
		}
		return fake
	}

	Describe("on a bucket whose flags carry object lock without BUCKET_VERSIONED, on memstore", func() {
		BeforeEach(func(ctx SpecContext) { f.setBucketFlags(ctx, meta.BucketObjLockEnabled) })

		DescribeTable("keeps a version verify_object_lock refuses and removes any other, checking versionId=null",
			func(ctx SpecContext, viaMulti bool, mode string, until time.Time, header, allowed bool, hold string, locked bool) {
				fake := bypassFake(func(*op.Request) bool { return allowed })
				f.env.Authz = fake
				lock(ctx, "small", mode, until, hold)
				var err error
				if viaMulti {
					err = multi(ctx, f.alice, header, small(meta.NullInstance))["small"]
				} else {
					_, err = del(ctx, f.alice, small(meta.NullInstance), header)
				}
				if locked {
					Expect(err).To(MatchError(op.ErrAccessDenied))
					if !viaMulti {
						expectForbidden(err)
					}
					Expect(f.stat(ctx, "small").Exists).To(BeTrue(), "the version is kept")
				} else {
					Expect(err).NotTo(HaveOccurred())
					Expect(f.stat(ctx, "small").Exists).To(BeFalse(), "the version is removed")
				}
				want := 0
				if header {
					want = 1
				}
				Expect(fake.VerifyBucketOwnerOrPolicyCallCount()).To(Equal(want), "the bypass is evaluated only with the header")
			},
			deleteLockMatrix(),
		)
		DescribeTable("checks a delete naming no version, which removes the object outright",
			func(ctx SpecContext, viaMulti bool) {
				lock(ctx, "small", "COMPLIANCE", day(3), "")
				f.put(ctx, "open", []byte("x"))
				if viaMulti {
					errs := multi(ctx, f.alice, true, small(""), meta.ObjKey{Name: "open"})
					Expect(errs["small"]).To(MatchError(op.ErrAccessDenied))
					Expect(errs["open"]).NotTo(HaveOccurred())
				} else {
					_, err := del(ctx, f.alice, small(""), true)
					expectForbidden(err)
					_, err = del(ctx, f.alice, meta.ObjKey{Name: "open"}, true)
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(f.stat(ctx, "small").Exists).To(BeTrue())
				Expect(f.stat(ctx, "open").Exists).To(BeFalse())
			},
			Entry("DeleteObject", false),
			Entry("DeleteObjects", true),
		)
		It("holds the removal to the RADOS version the check read, and no Expect, which this path refuses", func(ctx SpecContext) {
			st := f.stat(ctx, "small")
			stub := &opfakes.FakeObjectStore{}
			stub.StatObjectReturns(st, nil)
			stub.DeleteObjectReturns(&op.DeleteObjectResult{}, nil)
			f.env.Objects = stub
			for _, key := range []meta.ObjKey{small(""), small(meta.NullInstance)} {
				_, err := del(ctx, f.alice, key, false)
				Expect(err).NotTo(HaveOccurred())
			}
			Expect(stub.DeleteObjectCallCount()).To(Equal(2))
			for i := range 2 {
				_, _, _, p := stub.DeleteObjectArgsForCall(i)
				Expect(p.AssertEpoch).To(Equal(st.Epoch))
				Expect(p.Expect).To(BeNil())
			}
		})
		It("re-checks a removal its RADOS version refused, against a retention written since", func(ctx SpecContext) {
			f.env.Objects = &lockOnFirstDelete{ObjectStore: f.store, lock: func(ctx context.Context) {
				lock(ctx, "small", "COMPLIANCE", day(3), "")
			}}
			_, err := del(ctx, f.alice, small(meta.NullInstance), false)
			expectForbidden(err)
			Expect(f.stat(ctx, "small").Exists).To(BeTrue())
		})
		It("passes a version that does not exist unchecked, radosgw's 204", func(ctx SpecContext) {
			_, err := del(ctx, f.alice, small("v1"), false)
			Expect(err).NotTo(HaveOccurred())
		})
		It("answers a retention that does not decode 500 UnknownError, radosgw's -EIO, and keeps the version", func(ctx SpecContext) {
			Expect(f.store.SetObjectAttrs(ctx, f.stat(ctx, "small"), map[string][]byte{meta.AttrObjectRetention: {2, 1, 30, 0, 0, 0, 10}}, nil)).To(Succeed())
			_, err := del(ctx, f.alice, small(meta.NullInstance), false)
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(op.AsError(err).Status).To(Equal(http.StatusInternalServerError))
			Expect(multi(ctx, f.alice, false, small(meta.NullInstance))["small"]).To(MatchError(op.ErrUnknown))
			Expect(f.stat(ctx, "small").Exists).To(BeTrue())
		})

		// small holds a GOVERNANCE retention to 2030-01-03 and the tag team=a.
		Describe("through the policy evaluator", func() {
			BeforeEach(func(ctx SpecContext) {
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				lock(ctx, "small", "GOVERNANCE", day(3), "")
				var set tags.Set
				Expect(set.Add("team", "a", tags.MaxObjectTags)).To(Succeed())
				Expect(f.store.SetObjectAttrs(ctx, f.stat(ctx, "small"), map[string][]byte{tags.Attr: encodeTags(set)}, nil)).To(Succeed())
			})
			statement := func(effect, principal, action, resource, cond string) string {
				c := ""
				if cond != "" {
					c = `,"Condition":` + cond
				}
				return fmt.Sprintf(`{"Effect":%q,"Principal":{"AWS":[%q]},"Action":%q,"Resource":%q%s}`, effect, principal, action, resource, c)
			}
			setPolicy := func(ctx context.Context, statements ...string) {
				GinkgoHelper()
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte(`{"Version":"2012-10-17","Statement":[` +
					strings.Join(statements, ",") + `]}`)})
			}
			// deleteBoth deletes small's null version as id with the header
			// through DeleteObject, then through DeleteObjects, and returns
			// both answers.
			deleteBoth := func(ctx context.Context, id op.Identity) (single, perKey error) {
				_, single = del(ctx, id, small(meta.NullInstance), true)
				return single, multi(ctx, id, true, small(meta.NullInstance))["small"]
			}
			const (
				alice = "arn:aws:iam:::user/alice"
				bob   = "arn:aws:iam:::user/bob"
			)

			It("lets the owner bypass GOVERNANCE with the header and no policy", func(ctx SpecContext) {
				_, err := del(ctx, f.alice, small(meta.NullInstance), true)
				Expect(err).NotTo(HaveOccurred())
				Expect(f.stat(ctx, "small").Exists).To(BeFalse())
			})
			It("lets the owner's DeleteObjects key bypass GOVERNANCE with the header and no policy", func(ctx SpecContext) {
				Expect(multi(ctx, f.alice, true, small(meta.NullInstance))["small"]).NotTo(HaveOccurred())
				Expect(f.stat(ctx, "small").Exists).To(BeFalse())
			})
			DescribeTable("refuses the bypass a Deny on the bucket's objects binds, for a DeleteObjects key as for DeleteObject",
				func(ctx SpecContext, statements ...string) {
					setPolicy(ctx, statements...)
					single, perKey := deleteBoth(ctx, f.alice)
					expectForbidden(single)
					Expect(perKey).To(MatchError(op.ErrAccessDenied))
					Expect(f.stat(ctx, "small").Exists).To(BeTrue())
				},
				Entry("a Deny on arn:aws:s3:::plain/*, where radosgw's DeleteObjects evaluates the bucket's ARN",
					statement("Deny", alice, "s3:BypassGovernanceRetention", "arn:aws:s3:::plain/*", "")),
				Entry("a Deny on the object's own ARN", statement("Deny", alice, "s3:BypassGovernanceRetention", "arn:aws:s3:::plain/small", "")),
				Entry("a Deny conditioned on a tag the version carries, read from the state the check read",
					statement("Deny", alice, "s3:BypassGovernanceRetention", "arn:aws:s3:::plain/*", `{"StringEquals":{"s3:ExistingObjectTag/team":"a"}}`)),
			)
			It("evaluates the bypass on the key's ARN, not the bucket's, so a Deny on the bucket's ARN alone binds neither delete", func(ctx SpecContext) {
				setPolicy(ctx, statement("Deny", alice, "s3:BypassGovernanceRetention", "arn:aws:s3:::plain", ""))
				Expect(multi(ctx, f.alice, true, small(meta.NullInstance))["small"]).NotTo(HaveOccurred())
				Expect(f.stat(ctx, "small").Exists).To(BeFalse())
			})
			Describe("a grantee of the bucket ACL's WRITE", func() {
				BeforeEach(func(ctx SpecContext) {
					p := acl.DefaultPolicy(f.alice.Owner, "Alice")
					p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWrite})
					f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(p)})
				})
				It("deletes a version under no lock, as the grant authorizes s3:DeleteObjectVersion", func(ctx SpecContext) {
					f.put(ctx, "open", []byte("x"))
					_, err := del(ctx, f.bob, meta.ObjKey{Name: "open", Instance: meta.NullInstance}, true)
					Expect(err).NotTo(HaveOccurred())
					Expect(f.stat(ctx, "open").Exists).To(BeFalse())
				})
				It("gets no governance bypass from the grant, where radosgw's falls back to the bucket ACL's WRITE", func(ctx SpecContext) {
					single, perKey := deleteBoth(ctx, f.bob)
					expectForbidden(single)
					Expect(perKey).To(MatchError(op.ErrAccessDenied))
					Expect(f.stat(ctx, "small").Exists).To(BeTrue())
				})
				It("bypasses once a policy allows him s3:BypassGovernanceRetention on the object", func(ctx SpecContext) {
					setPolicy(ctx, statement("Allow", bob, "s3:BypassGovernanceRetention", "arn:aws:s3:::plain/*", ""))
					_, err := del(ctx, f.bob, small(meta.NullInstance), true)
					Expect(err).NotTo(HaveOccurred())
					Expect(f.stat(ctx, "small").Exists).To(BeFalse())
				})
			})
			It("gives a grantee of the object ACL's WRITE no bypass", func(ctx SpecContext) {
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(bobWriteGrant)
				Expect(f.store.SetObjectAttrs(ctx, f.stat(ctx, "small"), map[string][]byte{meta.AttrACL: encodedPolicy(p)}, nil)).To(Succeed())
				setPolicy(ctx, statement("Allow", bob, "s3:DeleteObjectVersion", "arn:aws:s3:::plain/*", ""))
				single, perKey := deleteBoth(ctx, f.bob)
				expectForbidden(single)
				Expect(perKey).To(MatchError(op.ErrAccessDenied))
				Expect(f.stat(ctx, "small").Exists).To(BeTrue())
			})
		})
	})

	Describe("on a versioned bucket with object lock, through a store fake", func() {
		var (
			stub   *opfakes.FakeObjectStore
			states []*op.ObjectState
		)
		// state is a version of small that exists, with attrs.
		state := func(instance string, attrs map[string][]byte) *op.ObjectState {
			return &op.ObjectState{Bucket: f.rec, Key: small(instance), Exists: true, Epoch: 7, Attrs: attrs}
		}
		compliance := func() map[string][]byte {
			return map[string][]byte{meta.AttrObjectRetention: retentionBytes("COMPLIANCE", day(3))}
		}
		governance := func() map[string][]byte {
			return map[string][]byte{meta.AttrObjectRetention: retentionBytes("GOVERNANCE", day(3))}
		}
		BeforeEach(func(ctx SpecContext) {
			f.setBucketFlags(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled)
			states = nil
			stub = &opfakes.FakeObjectStore{}
			// StatObject answers states in turn, the last for every read past
			// them.
			stub.StatObjectStub = func(context.Context, *op.BucketRecord, meta.ObjKey) (*op.ObjectState, error) {
				n := min(stub.StatObjectCallCount(), len(states)) - 1
				return states[n], nil
			}
			stub.DeleteObjectReturns(&op.DeleteObjectResult{VersionID: "v1"}, nil)
			f.env.Objects = stub
		})
		expectNoDelete := func() {
			GinkgoHelper()
			Expect(stub.DeleteObjectCallCount()).To(BeZero(), "the version is kept")
		}
		params := func(i int) op.DeleteParams {
			GinkgoHelper()
			_, _, _, p := stub.DeleteObjectArgsForCall(i)
			return p
		}

		DescribeTable("reads the version a delete names and holds its removal to the state read",
			func(ctx SpecContext, viaMulti bool, instance string) {
				states = []*op.ObjectState{state(instance, nil)}
				if viaMulti {
					Expect(multi(ctx, f.alice, false, small(instance))["small"]).NotTo(HaveOccurred())
				} else {
					_, err := del(ctx, f.alice, small(instance), false)
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(stub.StatObjectCallCount()).To(Equal(1))
				_, rec, key := stub.StatObjectArgsForCall(0)
				Expect(rec.Info.Bucket.Name).To(Equal("plain"))
				Expect(key).To(Equal(small(instance)))
				_, _, key, p := stub.DeleteObjectArgsForCall(0)
				Expect(key).To(Equal(small(instance)))
				Expect(p.Expect).To(BeIdenticalTo(states[0]))
				Expect(p.AssertEpoch).To(BeZero())
			},
			Entry("DeleteObject of a version", false, "v1"),
			Entry("DeleteObject of the null version", false, meta.NullInstance),
			Entry("a DeleteObjects key naming a version", true, "v1"),
			Entry("a DeleteObjects key naming the null version", true, meta.NullInstance),
		)
		DescribeTable("refuses a locked version, never reaching the store's delete",
			func(ctx SpecContext, viaMulti bool, attrs map[string][]byte) {
				states = []*op.ObjectState{state("v1", attrs)}
				if viaMulti {
					Expect(multi(ctx, f.alice, false, small("v1"))["small"]).To(MatchError(op.ErrAccessDenied))
				} else {
					_, err := del(ctx, f.alice, small("v1"), false)
					expectForbidden(err)
				}
				expectNoDelete()
			},
			Entry("COMPLIANCE, DeleteObject", false, compliance()),
			Entry("GOVERNANCE without the bypass, DeleteObject", false, governance()),
			Entry("a legal hold, DeleteObjects", true, map[string][]byte{meta.AttrObjectLegalHold: legalHoldBytes("ON")}),
			Entry("COMPLIANCE, DeleteObjects", true, compliance()),
		)
		It("removes a delete marker's own version unchecked, holding the removal to the marker's state", func(ctx SpecContext) {
			marker := &op.ObjectState{Bucket: f.rec, Key: small("m1"), DeleteMarker: true, VersionedEpoch: 5, Mtime: lockNow}
			states = []*op.ObjectState{marker}
			fake := bypassFake(func(*op.Request) bool { return true })
			f.env.Authz = fake
			_, err := del(ctx, f.alice, small("m1"), true)
			Expect(err).NotTo(HaveOccurred())
			Expect(params(0).Expect).To(BeIdenticalTo(marker))
			Expect(fake.VerifyBucketOwnerOrPolicyCallCount()).To(BeZero(), "a marker holds no lock to bypass")
		})
		It("holds the removal of a version that does not exist to the state that found none, which the store answers NoSuchKey, radosgw's 204", func(ctx SpecContext) {
			states = []*op.ObjectState{{Bucket: f.rec, Key: small("v9")}}
			stub.DeleteObjectReturns(nil, op.ErrNoSuchKey)
			_, err := del(ctx, f.alice, small("v9"), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(params(0).Expect).To(BeIdenticalTo(states[0]))
		})
		DescribeTable("reads nothing for a delete naming no version on an enabled bucket, whose delete marker goes over a locked version",
			func(ctx SpecContext, viaMulti bool) {
				states = []*op.ObjectState{state("", compliance())}
				if viaMulti {
					Expect(multi(ctx, f.alice, true, small(""))["small"]).NotTo(HaveOccurred())
				} else {
					_, err := del(ctx, f.alice, small(""), true)
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(stub.StatObjectCallCount()).To(BeZero())
				_, _, key, p := stub.DeleteObjectArgsForCall(0)
				Expect(key).To(Equal(small("")))
				Expect(p.Expect).To(BeNil())
			},
			Entry("DeleteObject", false),
			Entry("DeleteObjects", true),
		)
		Describe("on a suspended bucket", func() {
			BeforeEach(func(ctx SpecContext) { f.setBucketFlags(ctx, meta.BucketVersionsSuspended) })
			It("checks the null version a delete naming no version replaces with the null delete marker", func(ctx SpecContext) {
				states = []*op.ObjectState{state(meta.NullInstance, compliance())}
				_, err := del(ctx, f.alice, small(""), false)
				expectForbidden(err)
				expectNoDelete()
				_, _, key := stub.StatObjectArgsForCall(0)
				Expect(key).To(Equal(small(meta.NullInstance)))
			})
			It("holds the null delete marker to the null version it replaces", func(ctx SpecContext) {
				states = []*op.ObjectState{state(meta.NullInstance, nil)}
				Expect(multi(ctx, f.alice, false, small(""))["small"]).NotTo(HaveOccurred())
				_, _, key, p := stub.DeleteObjectArgsForCall(0)
				Expect(key).To(Equal(small("")), "the delete still names no version")
				Expect(p.Expect).To(BeIdenticalTo(states[0]))
			})
			It("holds the null delete marker to the read that found no null version", func(ctx SpecContext) {
				states = []*op.ObjectState{{Bucket: f.rec, Key: small(meta.NullInstance)}}
				_, err := del(ctx, f.alice, small(""), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(params(0).Expect).To(BeIdenticalTo(states[0]))
			})
		})
		It("answers a failed read as the delete's error, for DeleteObject and a DeleteObjects key", func(ctx SpecContext) {
			stub.StatObjectStub = nil
			stub.StatObjectReturns(nil, op.ErrServiceUnavailable)
			_, err := del(ctx, f.alice, small("v1"), false)
			Expect(err).To(MatchError(op.ErrServiceUnavailable))
			Expect(multi(ctx, f.alice, false, small("v1"))["small"]).To(MatchError(op.ErrServiceUnavailable))
			expectNoDelete()
		})

		Describe("after a refused removal", func() {
			It("reads the version again and refuses it when it is locked now", func(ctx SpecContext) {
				states = []*op.ObjectState{state("v1", nil), state("v1", compliance())}
				stub.DeleteObjectReturns(nil, op.ErrConcurrentModification)
				_, err := del(ctx, f.alice, small("v1"), false)
				expectForbidden(err)
				Expect(stub.DeleteObjectCallCount()).To(Equal(1))
				Expect(stub.StatObjectCallCount()).To(Equal(2))
			})
			DescribeTable("retries the delete held to the state read again",
				func(ctx SpecContext, viaMulti bool) {
					states = []*op.ObjectState{state("v1", nil), state("v1", map[string][]byte{meta.AttrACL: f.aliceACL})}
					stub.DeleteObjectReturnsOnCall(0, nil, op.ErrConcurrentModification)
					if viaMulti {
						Expect(multi(ctx, f.alice, false, small("v1"))["small"]).NotTo(HaveOccurred())
					} else {
						o, err := del(ctx, f.alice, small("v1"), false)
						Expect(err).NotTo(HaveOccurred())
						Expect(o.VersionID).To(Equal("v1"), "the retry's removal answers")
					}
					Expect(stub.DeleteObjectCallCount()).To(Equal(2))
					Expect(params(0).Expect).To(BeIdenticalTo(states[0]))
					Expect(params(1).Expect).To(BeIdenticalTo(states[1]))
				},
				Entry("DeleteObject", false),
				Entry("DeleteObjects", true),
			)
			It("gives up past the bound, answering DeleteObject's lost race as radosgw does, 204", func(ctx SpecContext) {
				states = []*op.ObjectState{state("v1", nil)}
				stub.DeleteObjectReturns(nil, op.ErrConcurrentModification)
				o, err := del(ctx, f.alice, small("v1"), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(o.VersionID).To(BeEmpty())
				Expect(stub.DeleteObjectCallCount()).To(Equal(lockedDeleteAttempts))
			})
			It("gives up past the bound, answering a DeleteObjects key's lost race as its error", func(ctx SpecContext) {
				states = []*op.ObjectState{state("v1", nil)}
				stub.DeleteObjectReturns(nil, op.ErrConcurrentModification)
				Expect(multi(ctx, f.alice, false, small("v1"))["small"]).To(MatchError(op.ErrConcurrentModification))
				Expect(stub.DeleteObjectCallCount()).To(Equal(lockedDeleteAttempts))
			})
			It("evaluates the bypass again on the state read again", func(ctx SpecContext) {
				states = []*op.ObjectState{state("v1", governance()), state("v1", governance())}
				stub.DeleteObjectReturnsOnCall(0, nil, op.ErrConcurrentModification)
				fake := bypassFake(func(*op.Request) bool { return true })
				f.env.Authz = fake
				_, err := del(ctx, f.alice, small("v1"), true)
				Expect(err).NotTo(HaveOccurred())
				Expect(fake.VerifyBucketOwnerOrPolicyCallCount()).To(Equal(2))
				for i, want := range states {
					_, r, _, _ := fake.VerifyBucketOwnerOrPolicyArgsForCall(i)
					Expect(r.ObjState).To(BeIdenticalTo(want))
				}
			})
			DescribeTable("reads again a version the first read found none of, and refuses it when a lock came with it",
				func(ctx SpecContext, viaMulti bool) {
					states = []*op.ObjectState{{Bucket: f.rec, Key: small("v9")}, state("v9", compliance())}
					stub.DeleteObjectReturns(nil, op.ErrConcurrentModification)
					if viaMulti {
						Expect(multi(ctx, f.alice, false, small("v9"))["small"]).To(MatchError(op.ErrAccessDenied))
					} else {
						_, err := del(ctx, f.alice, small("v9"), false)
						expectForbidden(err)
					}
					Expect(params(0).Expect).To(BeIdenticalTo(states[0]))
					Expect(stub.StatObjectCallCount()).To(Equal(2))
					Expect(stub.DeleteObjectCallCount()).To(Equal(1), "the version is kept")
				},
				Entry("DeleteObject", false),
				Entry("DeleteObjects", true),
			)
		})

		Describe("the bypass permission", func() {
			It("is not evaluated without the header, for either delete", func(ctx SpecContext) {
				states = []*op.ObjectState{state("v1", governance())}
				fake := bypassFake(func(*op.Request) bool { return true })
				f.env.Authz = fake
				_, err := del(ctx, f.alice, small("v1"), false)
				expectForbidden(err)
				Expect(multi(ctx, f.alice, false, small("v1"))["small"]).To(MatchError(op.ErrAccessDenied))
				Expect(fake.VerifyBucketOwnerOrPolicyCallCount()).To(BeZero())
				Expect(fake.VerifyBucketCallCount()).To(Equal(2), "the delete's own permission and DeleteObjects' request check")
				Expect(fake.VerifyBucketInCallCount()).To(Equal(1), "the key's permission")
				expectNoDelete()
			})
			It("is s3:BypassGovernanceRetention on the version's object, WRITE its ACL permission, with the state the check read", func(ctx SpecContext) {
				states = []*op.ObjectState{state("v1", governance())}
				fake := bypassFake(func(*op.Request) bool { return true })
				f.env.Authz = fake
				r := f.req(http.MethodDelete, "plain", "small")
				r.Object = small("v1")
				Expect(op.Run(ctx, &op.DeleteObject{BypassHeader: true}, r)).To(Succeed())
				Expect(fake.VerifyBucketOwnerOrPolicyCallCount()).To(Equal(1))
				_, br, a, perm := fake.VerifyBucketOwnerOrPolicyArgsForCall(0)
				Expect(a).To(Equal(policy.S3BypassGovernanceRetention))
				Expect(perm).To(Equal(acl.PermWrite), "rgw_iam_policy.h op_to_perm")
				Expect(br.Object).To(Equal(small("v1")))
				Expect(br.ObjState).To(BeIdenticalTo(states[0]))
				Expect(br.Identity).To(Equal(r.Identity))
				Expect(br.BucketRec).To(BeIdenticalTo(r.BucketRec))
				Expect(br).NotTo(BeIdenticalTo(r))
				Expect(r.ObjState).To(BeNil(), "the request itself is left as it was")
				Expect(stub.DeleteObjectCallCount()).To(Equal(1))
			})
			It("is evaluated for each DeleteObjects key on its own ARN", func(ctx SpecContext) {
				states = []*op.ObjectState{state("v1", governance())}
				fake := bypassFake(func(r *op.Request) bool { return r.Object.Name != "a" })
				f.env.Authz = fake
				errs := multi(ctx, f.alice, true, meta.ObjKey{Name: "a", Instance: "v1"}, meta.ObjKey{Name: "b", Instance: "v1"})
				Expect(errs["a"]).To(MatchError(op.ErrAccessDenied))
				Expect(errs["b"]).NotTo(HaveOccurred())
				Expect(fake.VerifyBucketOwnerOrPolicyCallCount()).To(Equal(2))
				var names []string
				for i := range 2 {
					_, r, _, _ := fake.VerifyBucketOwnerOrPolicyArgsForCall(i)
					names = append(names, r.Object.Name)
				}
				Expect(names).To(ConsistOf("a", "b"))
				Expect(stub.DeleteObjectCallCount()).To(Equal(1))
				_, _, key, _ := stub.DeleteObjectArgsForCall(0)
				Expect(key.Name).To(Equal("b"))
			})
			DescribeTable("takes a refusal of any kind for no bypass and fails nothing on its account",
				func(ctx SpecContext, refusal error) {
					states = []*op.ObjectState{state("v1", governance())}
					fake := &opfakes.FakeAuthorizer{}
					fake.VerifyBucketOwnerOrPolicyReturns(refusal)
					f.env.Authz = fake
					_, err := del(ctx, f.alice, small("v1"), true)
					expectForbidden(err)
					expectNoDelete()
				},
				Entry("an access denial", op.ErrAccessDenied),
				Entry("a fault, tags it could not read", fmt.Errorf("%w: tags unreadable", op.ErrInternalError)),
				Entry("a refusal marked before verify_permission", op.BeforeVerify(op.ErrUserSuspended)),
			)
			It("is not evaluated for a requester the delete's permission refused and op.Run lets through, stricter than radosgw, whose bypass_perm stays true for it", func(ctx SpecContext) {
				states = []*op.ObjectState{state("v1", governance())}
				fake := bypassFake(func(*op.Request) bool { return true })
				fake.VerifyBucketReturns(op.ErrAccessDenied)
				f.env.Authz = fake
				admin := f.alice
				admin.Admin = true
				_, err := del(ctx, admin, small("v1"), true)
				expectForbidden(err)
				Expect(fake.VerifyBucketOwnerOrPolicyCallCount()).To(BeZero())
				expectNoDelete()
			})
		})
	})

	// The store fake answers every read with a COMPLIANCE version, so a check
	// made where none belongs would read it and refuse the delete.
	DescribeTable("reads nothing and checks nothing on a bucket without object lock",
		func(ctx SpecContext, flags uint32) {
			f.setBucketFlags(ctx, flags)
			stub := &opfakes.FakeObjectStore{}
			stub.StatObjectReturns(&op.ObjectState{
				Bucket: f.rec, Key: small(meta.NullInstance), Exists: true, Epoch: 7,
				Attrs: map[string][]byte{meta.AttrObjectRetention: retentionBytes("COMPLIANCE", day(3))},
			}, nil)
			stub.DeleteObjectReturns(&op.DeleteObjectResult{}, nil)
			f.env.Objects = stub
			fake := bypassFake(func(*op.Request) bool { return true })
			f.env.Authz = fake
			for _, key := range []meta.ObjKey{small(""), small(meta.NullInstance), small("v1")} {
				_, err := del(ctx, f.alice, key, true)
				Expect(err).NotTo(HaveOccurred(), "DeleteObject of %q", key.Instance)
			}
			keys := []meta.ObjKey{{Name: "a"}, {Name: "b", Instance: meta.NullInstance}, {Name: "c", Instance: "v1"}}
			Expect(multi(ctx, f.alice, true, keys...)).To(Equal(map[string]error{"a": nil, "b": nil, "c": nil}))
			Expect(stub.StatObjectCallCount()).To(BeZero())
			Expect(fake.VerifyBucketOwnerOrPolicyCallCount()).To(BeZero())
			Expect(stub.DeleteObjectCallCount()).To(Equal(6))
			for i := range stub.DeleteObjectCallCount() {
				_, _, _, p := stub.DeleteObjectArgsForCall(i)
				Expect(p.Expect).To(BeNil())
				Expect(p.AssertEpoch).To(BeZero())
			}
		},
		Entry("a versioned bucket", uint32(meta.BucketVersioned)),
		Entry("a bucket whose versioning was never enabled", uint32(0)),
	)
})

// lockOnFirstDelete is an object store whose first DeleteObject runs lock
// before it reaches the store, as a retention written between the check and
// the removal lands, moving the version's RADOS version past the one the
// removal asserts.
type lockOnFirstDelete struct {
	op.ObjectStore
	lock    func(context.Context)
	deletes int
}

func (l *lockOnFirstDelete) DeleteObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) (*op.DeleteObjectResult, error) {
	l.deletes++
	if l.deletes == 1 {
		l.lock(ctx)
	}
	return l.ObjectStore.DeleteObject(ctx, rec, key, p)
}
