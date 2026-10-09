// Package conformance declares the store-conformance specs: the behaviors
// every implementation of op's store interfaces shares, phrased only through
// those interfaces, so that memstore's suite and the RADOS driver's
// integration suite run the same specs and a difference between the two
// fails one of them.
package conformance

import (
	"context"
	"crypto/rand"
	"strings"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // Ginkgo's DSL is meant to be dot-imported
	. "github.com/onsi/gomega"    //nolint:revive // Gomega's DSL is meant to be dot-imported

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// attrProbe is the bucket attr the attr specs write, which no reader
// interprets.
const attrProbe = "user.rgw.rgwgo-conformance"

// UsageLog is what the usage spec needs of an Env's Usage: the logger, and a
// view of what reached the store's usage log once flushed.
type UsageLog interface {
	op.UsageLogger
	// Logged returns the usage-log records of user, in its string form, and
	// bucket that the entries logged so far make, one per hour as the usage
	// log merges them. How the entries reach it is the adapter's: one over a
	// store that holds entries until a flush makes the store flush them
	// first, or reads a store that flushes each entry as it is logged.
	Logged(ctx context.Context, user, bucket string) ([]op.UsageRecord, error)
}

// Run declares the store-conformance specs against an op.Env factory: the
// behaviors every implementation of the store interfaces shares (users,
// buckets, listing, stats, usage). memstore's suite and the driver's
// integration suite both call it.
//
// newEnv returns an Env whose Usage is a UsageLog, and a function that
// releases it once the spec has removed what it created. Every name a spec
// creates starts with a prefix of its own, so specs share a store without
// meeting, and each spec removes what it created.
func Run(newEnv func(ctx context.Context) (*op.Env, func())) {
	Describe("store conformance", func() {
		var (
			env    *op.Env
			prefix string
		)
		BeforeEach(func(ctx SpecContext) {
			var done func()
			env, done = newEnv(ctx)
			DeferCleanup(done)
			prefix = "rgwgo-" + strings.ToLower(rand.Text()[:10])
		})

		// newUser puts a user named prefix-suffix with one access key and
		// an email, and removes it when the spec ends, after the buckets the
		// spec created for it.
		newUser := func(ctx context.Context, suffix string) *op.UserRecord {
			GinkgoHelper()
			id := meta.UserID{ID: prefix + "-" + suffix}
			key := rand.Text()[:20]
			info := meta.NewUserInfo()
			info.UserID = id
			info.DisplayName = "Conformance " + suffix
			info.Email = id.ID + "@example.com"
			info.AccessKeys = map[string]meta.AccessKey{key: {ID: key, Secret: rand.Text(), Active: true}}
			rec := &op.UserRecord{Info: info}
			Expect(env.Users.PutUser(ctx, rec, op.PutUserOptions{Exclusive: true})).To(Succeed(), "putting %s", id)
			DeferCleanup(func(ctx context.Context) {
				cur, err := env.Users.GetUser(ctx, id)
				Expect(err).NotTo(HaveOccurred(), "reading %s back to remove it", id)
				Expect(env.Users.RemoveUser(ctx, cur)).To(Succeed(), "removing %s", id)
			})
			return rec
		}

		ownerOf := func(u *op.UserRecord) meta.Owner { return meta.UserOwner(u.Info.UserID) }

		aclOf := func(u *op.UserRecord) []byte {
			e := denc.NewEncoder()
			acl.DefaultPolicy(ownerOf(u), u.Info.DisplayName).Encode(e, env.Zone.Release())
			return e.Bytes()
		}

		createParams := func(u *op.UserRecord, name string, quota meta.Quota) op.CreateBucketParams {
			zg := env.Zone.ZoneGroup()
			return op.CreateBucketParams{
				Name: name, Owner: ownerOf(u), Zonegroup: zg.ID, Placement: zg.DefaultPlacement,
				Attrs: map[string][]byte{meta.AttrACL: aclOf(u)}, Quota: quota, Exclusive: true,
			}
		}

		// newBucket creates the bucket for u and, when the spec ends,
		// removes its objects and then it.
		newBucket := func(ctx context.Context, u *op.UserRecord, name string, quota meta.Quota) *op.BucketRecord {
			GinkgoHelper()
			rec, err := env.Buckets.CreateBucket(ctx, createParams(u, name, quota))
			Expect(err).NotTo(HaveOccurred(), "creating %s", name)
			DeferCleanup(func(ctx context.Context) {
				cur, err := env.Buckets.GetBucket(ctx, "", name)
				Expect(err).NotTo(HaveOccurred(), "reading %s back to remove it", name)
				for {
					res, err := env.Buckets.ListObjects(ctx, cur, op.ListObjectsParams{MaxKeys: 1000})
					Expect(err).NotTo(HaveOccurred(), "listing %s to empty it", name)
					for i := range res.Entries {
						key := res.Entries[i].Key
						Expect(env.Objects.DeleteObject(ctx, cur, key, op.DeleteParams{})).To(Succeed(), "deleting %s/%s", name, key.Name)
					}
					if !res.Truncated {
						break
					}
				}
				Expect(env.Buckets.DeleteBucket(ctx, cur)).To(Succeed(), "deleting %s", name)
			})
			return rec
		}

		put := func(ctx context.Context, rec *op.BucketRecord, u *op.UserRecord, key, body string) {
			GinkgoHelper()
			_, err := env.Objects.PutObject(ctx, rec, meta.ObjKey{Name: key}, strings.NewReader(body),
				op.PutParams{Size: int64(len(body)), Attrs: map[string][]byte{meta.AttrACL: aclOf(u)}})
			Expect(err).NotTo(HaveOccurred(), "putting %s", key)
		}

		It("puts a user and reads it back by uid, access key and email", func(ctx SpecContext) {
			u := newUser(ctx, "u")
			var key string
			for k := range u.Info.AccessKeys {
				key = k
			}
			byID, err := env.Users.GetUser(ctx, u.Info.UserID)
			Expect(err).NotTo(HaveOccurred(), "by uid")
			byKey, err := env.Users.GetUserByAccessKey(ctx, key)
			Expect(err).NotTo(HaveOccurred(), "by access key")
			byEmail, err := env.Users.GetUserByEmail(ctx, u.Info.Email)
			Expect(err).NotTo(HaveOccurred(), "by email")
			for what, got := range map[string]*op.UserRecord{"by uid": byID, "by access key": byKey, "by email": byEmail} {
				Expect(got.Info.UserID).To(Equal(u.Info.UserID), what)
				Expect(got.Info.DisplayName).To(Equal(u.Info.DisplayName), what)
				Expect(got.Info.Email).To(Equal(u.Info.Email), what)
				Expect(got.Info.AccessKeys).To(HaveKeyWithValue(key, HaveField("Secret", u.Info.AccessKeys[key].Secret)), what)
				Expect(got.Version).To(Equal(u.Version), "%s: the version PutUser left in the record", what)
			}
		})

		It("finds a user by email in any case, as the email index is lower-cased", func(ctx SpecContext) {
			u := newUser(ctx, "mail")
			got, err := env.Users.GetUserByEmail(ctx, strings.ToUpper(u.Info.Email))
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.UserID).To(Equal(u.Info.UserID))
		})

		It("refuses an exclusive put of a user that exists", func(ctx SpecContext) {
			u := newUser(ctx, "twice")
			again := &op.UserRecord{Info: u.Info}
			Expect(env.Users.PutUser(ctx, again, op.PutUserOptions{Exclusive: true})).To(MatchError(op.ErrUserAlreadyExists))
		})

		// keyOf is the one access key newUser gave u.
		keyOf := func(u *op.UserRecord) string {
			GinkgoHelper()
			Expect(u.Info.AccessKeys).To(HaveLen(1))
			for k := range u.Info.AccessKeys {
				return k
			}
			return ""
		}

		It("finds a key's holder, active or not, and refuses its id to another user", func(ctx SpecContext) {
			u := newUser(ctx, "holder")
			key := keyOf(u)
			holder, err := env.Users.FindKeyHolder(ctx, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(holder).To(Equal(u.Info.UserID), "an active key")
			cur, err := env.Users.GetUser(ctx, u.Info.UserID)
			Expect(err).NotTo(HaveOccurred())
			k := cur.Info.AccessKeys[key]
			k.Active = false
			cur.Info.AccessKeys[key] = k
			Expect(env.Users.PutUser(ctx, cur, op.PutUserOptions{IfVersion: &cur.Version})).To(Succeed())
			holder, err = env.Users.FindKeyHolder(ctx, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(holder).To(Equal(u.Info.UserID), "a deactivated key")
			for _, active := range []bool{false, true} {
				other := meta.NewUserInfo()
				other.UserID = meta.UserID{ID: prefix + "-taker"}
				other.AccessKeys = map[string]meta.AccessKey{key: {ID: key, Secret: rand.Text(), Active: active}}
				err = env.Users.PutUser(ctx, &op.UserRecord{Info: other}, op.PutUserOptions{Exclusive: true})
				Expect(err).To(MatchError(op.ErrKeyExists), "active %t", active)
				Expect(err.Error()).NotTo(ContainSubstring(key), "the error names no key")
				_, err = env.Users.GetUser(ctx, other.UserID)
				Expect(err).To(MatchError(op.ErrNoSuchUser), "nothing written, active %t", active)
			}
		})

		It("releases a key's id once its holder drops it or is removed", func(ctx SpecContext) {
			u := newUser(ctx, "dropper")
			key := keyOf(u)
			cur, err := env.Users.GetUser(ctx, u.Info.UserID)
			Expect(err).NotTo(HaveOccurred())
			cur.Info.AccessKeys = nil
			Expect(env.Users.PutUser(ctx, cur, op.PutUserOptions{IfVersion: &cur.Version})).To(Succeed())
			_, err = env.Users.FindKeyHolder(ctx, key)
			Expect(err).To(MatchError(op.ErrNoSuchUser), "a dropped key")

			gone := meta.NewUserInfo()
			gone.UserID = meta.UserID{ID: prefix + "-gone"}
			gone.AccessKeys = map[string]meta.AccessKey{key: {ID: key, Secret: rand.Text(), Active: false}}
			rec := &op.UserRecord{Info: gone}
			Expect(env.Users.PutUser(ctx, rec, op.PutUserOptions{Exclusive: true})).To(Succeed(), "the id is free again")
			Expect(env.Users.RemoveUser(ctx, rec)).To(Succeed())
			_, err = env.Users.FindKeyHolder(ctx, key)
			Expect(err).To(MatchError(op.ErrNoSuchUser), "a removed user's key")
		})

		It("creates a bucket and reads it back with its attrs and both versions", func(ctx SpecContext) {
			u := newUser(ctx, "owner")
			name := prefix + "-b"
			rec := newBucket(ctx, u, name, meta.Quota{})
			got, err := env.Buckets.GetBucket(ctx, "", name)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket).To(Equal(rec.Info.Bucket), "the bucket")
			Expect(got.EntryPoint.Bucket).To(Equal(rec.Info.Bucket), "the entry point's bucket")
			Expect(got.Info.Owner).To(Equal(ownerOf(u)), "the owner")
			Expect(got.Info.PlacementRule).To(Equal(env.Zone.ZoneGroup().DefaultPlacement), "the placement rule")
			Expect(got.Attrs).To(HaveKeyWithValue(meta.AttrACL, aclOf(u)), "the ACL")
			Expect(got.Version.Ver).To(BeNumerically(">", 0), "the instance version")
			Expect(got.Version).To(Equal(rec.Version), "the instance version CreateBucket returned")
			Expect(got.EPVersion.Ver).To(BeNumerically(">", 0), "the entry point version")
			Expect(got.EPVersion).To(Equal(rec.EPVersion), "the entry point version CreateBucket returned")
		})

		It("answers a same-owner recreate with the existing record and BucketAlreadyExists", func(ctx SpecContext) {
			u := newUser(ctx, "owner")
			name := prefix + "-b"
			rec := newBucket(ctx, u, name, meta.Quota{})
			again, err := env.Buckets.CreateBucket(ctx, createParams(u, name, meta.Quota{}))
			Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(again).NotTo(BeNil(), "the existing record")
			Expect(again.Info.Bucket).To(Equal(rec.Info.Bucket), "the existing bucket")
			got, err := env.Buckets.GetBucket(ctx, "", name)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket.ID).To(Equal(rec.Info.Bucket.ID), "the name still names the first instance")
		})

		It("refuses PutBucketInfo under a stale version", func(ctx SpecContext) {
			u := newUser(ctx, "owner")
			name := prefix + "-b"
			newBucket(ctx, u, name, meta.Quota{})
			first, err := env.Buckets.GetBucket(ctx, "", name)
			Expect(err).NotTo(HaveOccurred())
			stale, err := env.Buckets.GetBucket(ctx, "", name)
			Expect(err).NotTo(HaveOccurred())
			Expect(env.Buckets.PutBucketInfo(ctx, first)).To(Succeed())
			Expect(first.Version.Ver).To(Equal(stale.Version.Ver+1), "the version moved on")
			Expect(env.Buckets.PutBucketInfo(ctx, stale)).To(MatchError(op.ErrConcurrentModification))
		})

		It("pages the owner's buckets in name order", func(ctx SpecContext) {
			u := newUser(ctx, "owner")
			for _, s := range []string{"c", "a", "b"} {
				newBucket(ctx, u, prefix+"-"+s, meta.Quota{})
			}
			names := func(ents []meta.BucketEnt) []string {
				var out []string
				for i := range ents {
					out = append(out, ents[i].Bucket.Name)
				}
				return out
			}
			ents, next, more, err := env.Users.ListUserBuckets(ctx, ownerOf(u), "", 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(names(ents)).To(Equal([]string{prefix + "-a", prefix + "-b"}), "the first page")
			Expect(more).To(BeTrue(), "more after the first page")
			Expect(next).To(Equal(prefix+"-b"), "the first page's next marker")
			ents, _, more, err = env.Users.ListUserBuckets(ctx, ownerOf(u), next, 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(names(ents)).To(Equal([]string{prefix + "-c"}), "the second page")
			Expect(more).To(BeFalse(), "more after the second page")
		})

		Context("ListObjects", func() {
			var rec *op.BucketRecord
			BeforeEach(func(ctx SpecContext) {
				u := newUser(ctx, "owner")
				rec = newBucket(ctx, u, prefix+"-list", meta.Quota{})
				for _, k := range []string{"e", "d/2", "a", "d/1"} {
					put(ctx, rec, u, k, k)
				}
			})
			list := func(ctx context.Context, p op.ListObjectsParams) (names []string, res op.ListObjectsResult) {
				GinkgoHelper()
				res, err := env.Buckets.ListObjects(ctx, rec, p)
				Expect(err).NotTo(HaveOccurred())
				for i := range res.Entries {
					names = append(names, res.Entries[i].Key.Name)
				}
				return names, res
			}

			It("rolls keys holding the delimiter into common prefixes", func(ctx SpecContext) {
				names, res := list(ctx, op.ListObjectsParams{Delimiter: "/", MaxKeys: 10})
				Expect(names).To(Equal([]string{"a", "e"}))
				Expect(res.CommonPrefixes).To(Equal([]string{"d/"}))
				Expect(res.Truncated).To(BeFalse())
			})

			It("starts after the marker", func(ctx SpecContext) {
				names, res := list(ctx, op.ListObjectsParams{Marker: "a", MaxKeys: 10})
				Expect(names).To(Equal([]string{"d/1", "d/2", "e"}))
				Expect(res.Truncated).To(BeFalse())
			})

			It("pages with a next marker", func(ctx SpecContext) {
				names, res := list(ctx, op.ListObjectsParams{MaxKeys: 2})
				Expect(names).To(Equal([]string{"a", "d/1"}))
				Expect(res.Truncated).To(BeTrue(), "truncated")
				Expect(res.NextMarker).To(Equal("d/1"), "the next marker")
			})

			It("returns nothing but truncation for max-keys 0", func(ctx SpecContext) {
				names, res := list(ctx, op.ListObjectsParams{MaxKeys: 0})
				Expect(names).To(BeEmpty(), "entries")
				Expect(res.CommonPrefixes).To(BeEmpty(), "common prefixes")
				Expect(res.Truncated).To(BeTrue(), "truncated")
			})
		})

		It("writes the record's attrs with PutBucketInfo, as put_info writes the bucket's attrs", func(ctx SpecContext) {
			u := newUser(ctx, "owner")
			name := prefix + "-info"
			newBucket(ctx, u, name, meta.Quota{})
			rec, err := env.Buckets.GetBucket(ctx, "", name)
			Expect(err).NotTo(HaveOccurred())
			rec.Attrs[attrProbe] = []byte("info")
			Expect(env.Buckets.PutBucketInfo(ctx, rec)).To(Succeed())
			got, err := env.Buckets.GetBucket(ctx, "", name)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Attrs).To(Equal(map[string][]byte{meta.AttrACL: aclOf(u), attrProbe: []byte("info")}))
		})

		It("lays PutBucketAttrs' set over the record's attrs, not the stored ones", func(ctx SpecContext) {
			u := newUser(ctx, "owner")
			name := prefix + "-attrs"
			newBucket(ctx, u, name, meta.Quota{})
			rec, err := env.Buckets.GetBucket(ctx, "", name)
			Expect(err).NotTo(HaveOccurred())
			rec.Attrs[attrProbe] = []byte("record")
			Expect(env.Buckets.PutBucketAttrs(ctx, rec, map[string][]byte{attrProbe + "-set": []byte("set")}, nil)).To(Succeed())
			want := map[string][]byte{meta.AttrACL: aclOf(u), attrProbe: []byte("record"), attrProbe + "-set": []byte("set")}
			Expect(rec.Attrs).To(Equal(want), "the caller's record")
			got, err := env.Buckets.GetBucket(ctx, "", name)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Attrs).To(Equal(want), "stored")
		})

		// set_attrs names the index entry's owner through decode_policy,
		// which decodes the ACL's owner alone (rgw_rados.cc:6694-6700 and
		// :1754-1768 at v19.2.6, :7494-7500 and :1857-1871 at v20.2.4).
		DescribeTable("names the owner of an object ACL that set_attrs wrote as decode_policy reads it",
			func(ctx SpecContext, policy func(u *op.UserRecord) []byte, owned bool) {
				u := newUser(ctx, "owner")
				rec := newBucket(ctx, u, prefix+"-acl", meta.Quota{})
				put(ctx, rec, u, "owned", "v")
				st, err := env.Objects.StatObject(ctx, rec, meta.ObjKey{Name: "owned"})
				Expect(err).NotTo(HaveOccurred())
				Expect(env.Objects.SetObjectAttrs(ctx, st, map[string][]byte{meta.AttrACL: policy(u)}, nil)).To(Succeed())
				res, err := env.Buckets.ListObjects(ctx, rec, op.ListObjectsParams{Prefix: "owned", MaxKeys: 1})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Entries).To(HaveLen(1))
				if owned {
					Expect(res.Entries[0].Owner).To(Equal(ownerOf(u)))
					Expect(res.Entries[0].OwnerDisplayName).To(Equal(u.Info.DisplayName))
				} else {
					Expect(res.Entries[0].Owner).To(Equal(meta.Owner{}))
					Expect(res.Entries[0].OwnerDisplayName).To(BeEmpty())
				}
			},
			Entry("grants that do not decode still name the owner", func(u *op.UserRecord) []byte {
				e := denc.NewEncoder()
				f := e.BeginStruct(2, 2)
				acl.Owner{ID: u.Info.UserID.String(), DisplayName: u.Info.DisplayName}.Encode(e, env.Zone.Release())
				e.Raw([]byte{0xff, 0xff})
				e.EndStruct(f)
				return e.Bytes()
			}, true),
			Entry("an owner that does not decode names none", func(*op.UserRecord) []byte { return []byte{1, 2, 3} }, false),
		)

		It("reports zero stats for an empty bucket", func(ctx SpecContext) {
			u := newUser(ctx, "owner")
			rec := newBucket(ctx, u, prefix+"-empty", meta.Quota{})
			Expect(env.Stats.BucketStats(ctx, rec)).To(Equal(op.Stats{}))
		})

		It("refuses the second object under a bucket quota of one object", func(ctx SpecContext) {
			u := newUser(ctx, "owner")
			quota := meta.Quota{Enabled: true, MaxObjects: 1, MaxSize: -1}
			rec := newBucket(ctx, u, prefix+"-quota", quota)
			Expect(env.Stats.CheckQuota(ctx, rec, ownerOf(u), 1, 1)).To(Succeed(), "the first object")
			put(ctx, rec, u, "one", "1")
			Expect(env.Stats.AdjustStats(ctx, rec, ownerOf(u), 1, 1, 0)).To(Succeed())
			Expect(env.Stats.CheckQuota(ctx, rec, ownerOf(u), 1, 1)).To(MatchError(op.ErrQuotaExceeded), "the second object")
		})

		It("logs usage the usage log shows once flushed", func(ctx SpecContext) {
			ul, ok := env.Usage.(UsageLog)
			Expect(ok).To(BeTrue(), "the Env's Usage, %T, is no conformance.UsageLog", env.Usage)
			u := newUser(ctx, "usage")
			bucket := prefix + "-usage"
			ul.Log(ctx, op.UsageEntry{
				Owner: ownerOf(u), Bucket: bucket, Time: env.Clock(), Category: "put_obj",
				BytesReceived: 4321, Ops: 1, SuccessfulOps: 1,
			})
			recs, err := ul.Logged(ctx, u.Info.UserID.String(), bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(ConsistOf(And(
				HaveField("Owner", u.Info.UserID.String()),
				HaveField("Bucket", bucket),
				HaveField("Categories", HaveKeyWithValue("put_obj", op.UsageData{BytesReceived: 4321, Ops: 1, SuccessfulOps: 1})),
			)))
		})
	})
}
