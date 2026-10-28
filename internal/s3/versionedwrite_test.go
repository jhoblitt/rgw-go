package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// newInstance matches a version id gen_rand_obj_instance_name draws.
const newInstance = `^[A-Za-z0-9.-]{31}$`

var _ = Describe("the writes that create a head on a versioned bucket", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			// writes are alice's three writes of k in plain, the query
			// appended to the target.
			var (
				w      *writeWorld
				writes map[string]func(query string) *httptest.ResponseRecorder
			)
			flagged := func(ctx context.Context, flags uint32) {
				GinkgoHelper()
				if flags != 0 {
					w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= flags })
				}
			}
			BeforeEach(func(ctx SpecContext) {
				w = newWriteWorld(ctx, rel)
				writes = map[string]func(query string) *httptest.ResponseRecorder{
					"PutObject": func(query string) *httptest.ResponseRecorder {
						return w.send(w.alice, http.MethodPut, "/plain/k"+query, "x")
					},
					"CopyObject": func(query string) *httptest.ResponseRecorder {
						return w.send(w.alice, http.MethodPut, "/plain/k"+query, "", "X-Amz-Copy-Source", "/plain/src")
					},
					"CompleteMultipartUpload": func(query string) *httptest.ResponseRecorder {
						GinkgoHelper()
						id := w.initUpload(w.alice, "/plain/k")
						Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
						target := "/plain/k?uploadId=" + id
						if query != "" {
							target += "&" + strings.TrimPrefix(query, "?")
						}
						return w.send(w.alice, http.MethodPost, target, completion(md5Hex("x")))
					},
				}
			})

			DescribeTable("send x-amz-version-id for the version written, null for the null version, none on a bucket never versioned",
				func(ctx SpecContext, flags uint32, query, want string) {
					flagged(ctx, flags)
					for name, write := range writes {
						rec := write(query)
						Expect(rec.Code).To(Equal(200), "%s: %s", name, rec.Body.String())
						switch want {
						case "":
							Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-version-id"), name)
						case newInstance:
							Expect(exactHeader(rec.Header(), "x-amz-version-id")).To(MatchRegexp(newInstance), name)
						default:
							Expect(exactHeader(rec.Header(), "x-amz-version-id")).To(Equal(want), name)
						}
					}
				},
				Entry("never versioned", uint32(0), "", ""),
				Entry("never versioned, the versionId=null a write ignores", uint32(0), "?versionId=null", ""),
				Entry("never versioned, the versionId a write ignores", uint32(0), "?versionId=v1", ""),
				Entry("enabled: a new version", uint32(meta.BucketVersioned), "", newInstance),
				Entry("enabled, the versionId a write ignores", uint32(meta.BucketVersioned), "?versionId=v1", newInstance),
				Entry("suspended: the null version", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), "", "null"),
				Entry("suspended, the versionId a write ignores", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), "?versionId=v1", "null"),
			)
			It("give each version an enabled bucket gets an id of its own, the instance each wrote", func(ctx SpecContext) {
				flagged(ctx, meta.BucketVersioned)
				seen := map[string]string{}
				for name, write := range writes {
					id := exactHeader(write("").Header(), "x-amz-version-id")
					Expect(id).To(MatchRegexp(newInstance), name)
					Expect(seen).NotTo(HaveKey(id), "%s's version id is new", name)
					seen[id] = name
					st, err := w.store.StatObject(ctx, w.bucket(ctx), meta.ObjKey{Name: "k", Instance: id})
					Expect(err).NotTo(HaveOccurred())
					Expect(st.Exists).To(BeTrue(), "%s wrote k at instance %s", name, id)
				}
			})
			It("answer a completion retried after it succeeded on an enabled bucket with the version the first wrote", func(ctx SpecContext) {
				flagged(ctx, meta.BucketVersioned)
				id := w.initUpload(w.alice, "/plain/k")
				Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
				first := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x")))
				Expect(first.Code).To(Equal(200), first.Body.String())
				version := exactHeader(first.Header(), "x-amz-version-id")
				Expect(version).To(MatchRegexp(newInstance))
				retry := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x")))
				Expect(retry.Code).To(Equal(200), retry.Body.String())
				Expect(exactHeader(retry.Header(), "x-amz-version-id")).To(Equal(version))
			})
			DescribeTable("write the plain object on a bucket never versioned, whatever versionId they name",
				func(ctx SpecContext, query string) {
					for name, write := range writes {
						Expect(write(query).Code).To(Equal(200), name)
						Expect(w.stat(ctx, "k").Exists).To(BeTrue(), "%s wrote the plain k", name)
						st, err := w.store.StatObject(ctx, w.bucket(ctx), meta.ObjKey{Name: "k", Instance: "v1"})
						Expect(err).NotTo(HaveOccurred())
						Expect(st.Exists).To(BeFalse(), "%s wrote no version v1", name)
						Expect(w.send(w.alice, http.MethodDelete, "/plain/k", "").Code).To(Equal(204))
					}
				},
				Entry("versionId=null, which radosgw files in the index under the instance null", "?versionId=null"),
				Entry("another versionId, which radosgw writes as that instance", "?versionId=v1"),
			)

			Describe("CopyObject's x-amz-copy-source-version-id", func() {
				// source is a bucket "versions" of alice's with flags, holding
				// "src" at the plain key and at instance v1.
				source := func(ctx context.Context, flags uint32) {
					GinkgoHelper()
					owner := meta.UserOwner(w.alice.Info.UserID)
					aliceACL := encodeACL(acl.DefaultPolicy(owner, "Alice"))
					rec := w.addBucket(ctx, "versions", owner, map[string][]byte{meta.AttrACL: aliceACL}, map[string][]byte{"src": aliceACL})
					_, err := w.store.PutObject(ctx, rec, meta.ObjKey{Name: "src", Instance: "v1"}, strings.NewReader("one"),
						op.PutParams{Size: 3, Attrs: map[string][]byte{meta.AttrACL: aliceACL}})
					Expect(err).NotTo(HaveOccurred())
					rec.Info.Flags |= flags
					Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
				}
				DescribeTable("names the version copied by its bucket's versioning, whatever the destination's",
					func(ctx SpecContext, flags uint32, src, want string) {
						source(ctx, flags)
						rec := w.send(w.alice, http.MethodPut, "/plain/k", "", "X-Amz-Copy-Source", src)
						Expect(rec.Code).To(Equal(200), rec.Body.String())
						Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-version-id"), "plain was never versioned")
						if want == "" {
							Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-copy-source-version-id"))
							return
						}
						Expect(exactHeader(rec.Header(), "x-amz-copy-source-version-id")).To(Equal(want))
					},
					Entry("a version named", uint32(meta.BucketVersioned), "/versions/src?versionId=v1", "v1"),
					Entry("the null version, read naming none", uint32(meta.BucketVersioned), "/versions/src", "null"),
					Entry("the null version named on a suspended bucket", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), "/versions/src?versionId=null", "null"),
					Entry("an object of a bucket never versioned", uint32(0), "/versions/src", ""),
					Entry("the null version named on a bucket never versioned", uint32(0), "/versions/src?versionId=null", ""),
				)
				It("copies the version named", func(ctx SpecContext) {
					source(ctx, meta.BucketVersioned)
					rec := w.send(w.alice, http.MethodPut, "/plain/k", "", "X-Amz-Copy-Source", "/versions/src?versionId=v1")
					Expect(rec.Code).To(Equal(200), rec.Body.String())
					Expect(w.stat(ctx, "k").ETag).To(Equal(md5Hex("one")))
				})
				It("copies k?versionId=null onto k on a bucket never versioned, keeping its data, with no check of a copy onto itself", func() {
					Expect(w.send(w.alice, http.MethodPut, "/plain/k", "data").Code).To(Equal(200))
					expectError(w.send(w.alice, http.MethodPut, "/plain/k", "", "X-Amz-Copy-Source", "/plain/k"), 400, "InvalidRequest")
					rec := w.send(w.alice, http.MethodPut, "/plain/k", "", "X-Amz-Copy-Source", "/plain/k?versionId=null")
					Expect(rec.Code).To(Equal(200), rec.Body.String())
					got := w.send(w.alice, http.MethodGet, "/plain/k", "")
					Expect(got.Code).To(Equal(200))
					Expect(got.Body.String()).To(Equal("data"))
				})
			})

			DescribeTable("answer 501 NotImplemented on a bucket with object lock and write nothing",
				func(ctx SpecContext, flags uint32) {
					flagged(ctx, flags)
					for name, write := range writes {
						expectError(write(""), 501, "NotImplemented")
						Expect(w.stat(ctx, "k").Exists).To(BeFalse(), name)
					}
				},
				Entry("versioned with object lock", uint32(meta.BucketVersioned|meta.BucketObjLockEnabled)),
				Entry("suspended with object lock", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended|meta.BucketObjLockEnabled)),
			)
		})
	}
})

var _ = Describe("an append", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, denc.Squid) })

	DescribeTable("is 409 InvalidBucketState on a bucket whose versioning was ever enabled, and stores nothing",
		func(ctx SpecContext, flags uint32) {
			w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= flags })
			expectError(w.send(w.alice, http.MethodPut, "/plain/k?append&position=0", "x"), 409, "InvalidBucketState")
			Expect(w.stat(ctx, "k").Exists).To(BeFalse())
		},
		Entry("enabled", uint32(meta.BucketVersioned)),
		Entry("suspended", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended)),
		Entry("with object lock", uint32(meta.BucketVersioned|meta.BucketObjLockEnabled)),
	)
	It("is 501 NotImplemented on a bucket never versioned", func(ctx SpecContext) {
		expectError(w.send(w.alice, http.MethodPut, "/plain/k?append&position=0", "x"), 501, "NotImplemented")
		Expect(w.stat(ctx, "k").Exists).To(BeFalse())
	})
	It("is 403 for a requester the bucket refuses, whatever its versioning", func(ctx SpecContext) {
		expectError(w.send(w.bob, http.MethodPut, "/plain/k?append&position=0", "x"), 403, "AccessDenied")
		w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketVersioned })
		expectError(w.send(w.bob, http.MethodPut, "/plain/k?append&position=0", "x"), 403, "AccessDenied")
	})
	DescribeTable("is InvalidArgument for a position strict_strtoll refuses or a negative one, as get_params reads it",
		func(ctx SpecContext, flags uint32, query string) {
			if flags != 0 {
				w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= flags })
			}
			expectError(w.send(w.alice, http.MethodPut, "/plain/k?append"+query, "x"), 400, "InvalidArgument")
		},
		Entry("no position", uint32(meta.BucketVersioned), ""),
		Entry("an empty position", uint32(meta.BucketVersioned), "&position="),
		Entry("a position with a byte after its digits", uint32(meta.BucketVersioned), "&position=1x"),
		Entry("a negative position", uint32(meta.BucketVersioned), "&position=-1"),
		Entry("a bad position on a bucket never versioned", uint32(0), "&position=x"),
	)
	It("reads a position up to a NUL, as get_params passes it as a C string", func(ctx SpecContext) {
		w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketVersioned })
		expectError(w.send(w.alice, http.MethodPut, "/plain/k?append&position=7%00x", "x"), 409, "InvalidBucketState")
	})
})
