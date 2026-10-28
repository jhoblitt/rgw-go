package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// versions is the object store a versioned key's reads meet once the
// driver follows its OLH: memstore keeps each version under its own key, and
// a read naming no version reads the instance current names for the key, ""
// for the null version. A delete marker, read as the current version or by
// its own version, is a version that does not exist marked DeleteMarker,
// under the key as named, as the driver's StatObject answers one; read by its
// version it carries markerMtime, its index entry's mtime.
type versions struct {
	op.ObjectStore
	current map[string]string
	markers map[meta.ObjKey]bool
}

func (v versions) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return v.read(ctx, rec, key, v.ObjectStore.StatObject)
}

func (v versions) PrefetchObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return v.read(ctx, rec, key, v.ObjectStore.PrefetchObject)
}

func (v versions) read(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey,
	read func(context.Context, *op.BucketRecord, meta.ObjKey) (*op.ObjectState, error),
) (*op.ObjectState, error) {
	target := key
	if key.Instance == "" {
		instance, ok := v.current[key.Name]
		if !ok {
			return read(ctx, rec, key)
		}
		target.Instance = instance
	}
	stored := target
	if stored.Instance == meta.NullInstance {
		stored.Instance = ""
	}
	if v.markers[stored] {
		st := &op.ObjectState{Bucket: rec, Key: key, DeleteMarker: true}
		if key.Instance != "" {
			st.Mtime = markerMtime
		}
		return st, nil
	}
	return read(ctx, rec, target)
}

// markerMtime is the mtime of a delete marker read by its version.
var markerMtime = time.Date(2026, 10, 1, 8, 9, 10, 0, time.UTC)

// bobPolicy is a bucket policy for plain whose statements, each an effect,
// an action and a resource under arn:aws:s3:::, name bob.
func bobPolicy(stmts ...[3]string) string {
	parts := make([]string, 0, len(stmts))
	for _, st := range stmts {
		parts = append(parts, `{"Effect":"`+st[0]+`","Principal":{"AWS":["arn:aws:iam:::user/bob"]},"Action":"`+st[1]+`","Resource":"arn:aws:s3:::`+st[2]+`"}`)
	}
	return `{"Version":"2012-10-17","Statement":[` + strings.Join(parts, ",") + `]}`
}

// expectMissingVersion checks rec is the 404 NoSuchKey of a missing version
// on rel: Tentacle's x-amz-delete-marker: false, none on Squid.
func expectMissingVersion(rec *httptest.ResponseRecorder, rel denc.Release, method string) {
	GinkgoHelper()
	Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
	if method == http.MethodGet {
		Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchKey</Code>"))
	}
	if rel >= denc.Tentacle {
		Expect(deleteMarkerHeaders(rec)).To(Equal([]string{"x-amz-delete-marker: false"}), "radosgw follows no olh for a named version")
	} else {
		Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"))
	}
}

// expectMarkerVersion checks rec is the 405 of a delete marker's version,
// with the marker's Last-Modified when lastModified, and without one
// otherwise.
func expectMarkerVersion(rec *httptest.ResponseRecorder, method string, lastModified bool) {
	GinkgoHelper()
	Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed), rec.Body.String())
	if method == http.MethodGet {
		Expect(rec.Body.String()).To(ContainSubstring("<Code>MethodNotAllowed</Code>"))
	}
	Expect(deleteMarkerHeaders(rec)).To(Equal([]string{"x-amz-delete-marker: true"}))
	Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-version-id"), "an error names no version")
	if lastModified {
		Expect(rec.Header().Values("Last-Modified")).To(Equal([]string{"Thu, 01 Oct 2026 08:09:10 GMT"}))
	} else {
		Expect(rec.Header()).NotTo(haveHeaderAnyCase("Last-Modified"))
	}
}

// putVersion stores key in plain at instance with data and alice's ACL.
func (w *writeWorld) putVersion(ctx context.Context, key, instance, data string) {
	GinkgoHelper()
	_, err := w.store.PutObject(ctx, w.bucket(ctx), meta.ObjKey{Name: key, Instance: instance}, strings.NewReader(data), op.PutParams{
		Size: int64(len(data)), Attrs: map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice"))},
	})
	Expect(err).NotTo(HaveOccurred())
}

// versionAttr is attr of key's version instance in plain.
func (w *writeWorld) versionAttr(ctx context.Context, key, instance, attr string) []byte {
	GinkgoHelper()
	st, err := w.store.StatObject(ctx, w.bucket(ctx), meta.ObjKey{Name: key, Instance: instance})
	Expect(err).NotTo(HaveOccurred())
	Expect(st.Exists).To(BeTrue(), "%s at %q", key, instance)
	return st.Attrs[attr]
}

// deleteMarkerHeaders are rec's x-amz-delete-marker lines, in any spelling.
func deleteMarkerHeaders(rec *httptest.ResponseRecorder) []string {
	var lines []string
	for k, vs := range rec.Header() {
		if strings.EqualFold(k, "x-amz-delete-marker") {
			for _, v := range vs {
				lines = append(lines, k+": "+v)
			}
		}
	}
	return lines
}

var _ = Describe("reads of a versioned key", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var w *writeWorld
			// versioned gives plain flags and has the world's store resolve
			// each name in current as the OLH would, with markers delete
			// markers.
			versioned := func(ctx context.Context, flags uint32, current map[string]string, markers ...meta.ObjKey) {
				GinkgoHelper()
				if flags != 0 {
					w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= flags })
				}
				v := versions{ObjectStore: w.store, current: current, markers: map[meta.ObjKey]bool{}}
				for _, m := range markers {
					v.markers[m] = true
				}
				w.env.Objects = v
			}
			BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, rel) })

			DescribeTable("name the version they read in x-amz-version-id, null for the null version",
				func(ctx SpecContext, flags uint32, setup func(ctx context.Context, w *writeWorld) map[string]string, target, body, want string) {
					versioned(ctx, flags, setup(ctx, w))
					for _, read := range []struct {
						method, target string
						hdr            []string
					}{
						{http.MethodGet, target, nil},
						{http.MethodHead, target, nil},
						{http.MethodGet, withArg(target, "attributes"), []string{"X-Amz-Object-Attributes", "ETag"}},
					} {
						t := read.target
						rec := w.send(w.alice, read.method, t, "", read.hdr...)
						Expect(rec.Code).To(Equal(http.StatusOK), "%s %s: %s", read.method, t, rec.Body.String())
						if read.method == http.MethodGet && read.hdr == nil {
							Expect(rec.Body.String()).To(Equal(body), "%s %s reads the version", read.method, t)
						}
						if want == "" {
							Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-version-id"), "%s %s", read.method, t)
							continue
						}
						Expect(exactHeader(rec.Header(), "x-amz-version-id")).To(Equal(want), "%s %s", read.method, t)
					}
				},
				Entry("an enabled bucket's current version, named by none", uint32(meta.BucketVersioned), twoVersions, "/plain/k", "two", "v2"),
				Entry("a version named", uint32(meta.BucketVersioned), twoVersions, "/plain/k?versionId=v1", "one", "v1"),
				Entry("a suspended bucket's null current version, named by none, where radosgw names none",
					uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), nullCurrent, "/plain/k", "null", "null"),
				Entry("an enabled bucket's object written before versioning, its null version, named by none",
					uint32(meta.BucketVersioned), nullCurrent, "/plain/k", "null", "null"),
				Entry("the null version named", uint32(meta.BucketVersioned), nullCurrent, "/plain/k?versionId=null", "null", "null"),
				Entry("an object of a bucket never versioned, which has no version", uint32(0), plainOnly, "/plain/k", "plain", ""),
				Entry("the null version named on a bucket never versioned, as radosgw names it", uint32(0), plainOnly, "/plain/k?versionId=null", "plain", "null"),
			)

			Describe("whose current version is a delete marker", func() {
				BeforeEach(func(ctx SpecContext) {
					w.putVersion(ctx, "k", "v1", "one")
					versioned(ctx, meta.BucketVersioned, map[string]string{"k": "m1"}, meta.ObjKey{Name: "k", Instance: "m1"})
				})
				DescribeTable("answer 404 NoSuchKey with x-amz-delete-marker: true, once, on both releases",
					func(method, target string, hdr ...string) {
						rec := w.send(w.alice, method, target, "", hdr...)
						Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
						if method == http.MethodGet {
							Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchKey</Code>"))
						}
						Expect(deleteMarkerHeaders(rec)).To(Equal([]string{"x-amz-delete-marker: true"}))
						Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-version-id"), "radosgw names no version on the 404")
						Expect(rec.Header()).NotTo(haveHeaderAnyCase("Last-Modified"))
					},
					Entry("GET", http.MethodGet, "/plain/k"),
					Entry("HEAD", http.MethodHead, "/plain/k"),
					Entry("GET ?acl", http.MethodGet, "/plain/k?acl"),
					Entry("GET ?tagging", http.MethodGet, "/plain/k?tagging"),
					Entry("GET ?attributes", http.MethodGet, "/plain/k?attributes", "X-Amz-Object-Attributes", "ETag"),
					Entry("ListParts of a missing upload, whose state is the key's once stated", http.MethodGet, "/plain/k?uploadId=2~nope"),
				)
				It("answer 403 AccessDenied without the header to a requester who may not list the bucket", func() {
					for _, m := range []string{http.MethodGet, http.MethodHead} {
						rec := w.send(w.bob, m, "/plain/k", "")
						Expect(rec.Code).To(Equal(http.StatusForbidden), "%s: %s", m, rec.Body.String())
						Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"), m)
					}
				})
				It("read a noncurrent version named as any other", func() {
					rec := w.send(w.alice, http.MethodGet, "/plain/k?versionId=v1", "")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Body.String()).To(Equal("one"))
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"))
				})
			})

			Describe("naming a delete marker's own version", func() {
				BeforeEach(func(ctx SpecContext) {
					w.putVersion(ctx, "k", "v1", "one")
					versioned(ctx, meta.BucketVersioned|meta.BucketVersionsSuspended, map[string]string{"k": "v1"},
						meta.ObjKey{Name: "k", Instance: "m1"}, meta.ObjKey{Name: "k"})
				})
				// policy puts plain's bucket policy for bob.
				policy := func(stmts ...[3]string) {
					GinkgoHelper()
					rec := w.send(w.alice, http.MethodPut, "/plain?policy", bobPolicy(stmts...))
					Expect(rec.Code).To(Equal(http.StatusNoContent), rec.Body.String())
				}
				DescribeTable("answer 405 MethodNotAllowed with x-amz-delete-marker: true, once, on both releases",
					func(method, target string, lastModified bool, hdr ...string) {
						expectMarkerVersion(w.send(w.alice, method, target, "", hdr...), method, lastModified)
					},
					Entry("GET, with the marker's Last-Modified", http.MethodGet, "/plain/k?versionId=m1", true),
					Entry("HEAD, with the marker's Last-Modified", http.MethodHead, "/plain/k?versionId=m1", true),
					Entry("GET naming versionId=null, whose null version is a delete marker", http.MethodGet, "/plain/k?versionId=null", true),
					Entry("HEAD naming versionId=null, whose null version is a delete marker", http.MethodHead, "/plain/k?versionId=null", true),
					Entry("GET ?attributes, without Last-Modified", http.MethodGet, "/plain/k?attributes&versionId=m1", false, "X-Amz-Object-Attributes", "ETag"),
					Entry("GET ?acl, without Last-Modified", http.MethodGet, "/plain/k?acl&versionId=m1", false),
					Entry("GET ?tagging, without Last-Modified", http.MethodGet, "/plain/k?tagging&versionId=m1", false),
				)
				It("answer 403 AccessDenied without the header to a requester who may neither list the bucket nor read the version", func() {
					for _, target := range []string{"/plain/k?versionId=m1", "/plain/k?acl&versionId=m1", "/plain/k?tagging&versionId=m1"} {
						for _, m := range []string{http.MethodGet, http.MethodHead} {
							rec := w.send(w.bob, m, target, "")
							Expect(rec.Code).To(Equal(http.StatusForbidden), "%s %s: %s", m, target, rec.Body.String())
							Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"), m)
						}
					}
				})
				DescribeTable("answer 404 NoSuchKey, as a missing version, to a requester who may list the bucket but not read the version",
					func(stmts ...[3]string) {
						policy(stmts...)
						for _, target := range []string{"/plain/k?versionId=m1", "/plain/k?versionId=null", "/plain/k?acl&versionId=m1", "/plain/k?tagging&versionId=m1"} {
							for _, m := range []string{http.MethodGet, http.MethodHead} {
								expectMissingVersion(w.send(w.bob, m, target, ""), rel, m)
							}
						}
						expectMissingVersion(w.send(w.bob, http.MethodGet, "/plain/k?attributes&versionId=m1", "", "X-Amz-Object-Attributes", "ETag"), rel, http.MethodGet)
					},
					Entry("allowed s3:ListBucket alone", [3]string{"Allow", "s3:ListBucket", "plain"}),
					Entry("allowed s3:ListBucket and denied s3:GetObjectVersion and the rest",
						[3]string{"Allow", "s3:ListBucket", "plain"}, [3]string{"Allow", "s3:GetObjectVersion*", "plain/*"},
						[3]string{"Deny", "s3:GetObjectVersion*", "plain/*"}),
				)
				It("answer 405 to a requester allowed the version's read who may not list the bucket, and 403 for a missing version", func() {
					policy([3]string{"Allow", "s3:GetObjectVersion", "plain/*"})
					for _, m := range []string{http.MethodGet, http.MethodHead} {
						expectMarkerVersion(w.send(w.bob, m, "/plain/k?versionId=m1", ""), m, true)
						rec := w.send(w.bob, m, "/plain/k?versionId=gone", "")
						Expect(rec.Code).To(Equal(http.StatusForbidden), "%s: %s", m, rec.Body.String())
						Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"), m)
					}
					rec := w.send(w.bob, http.MethodGet, "/plain/k?acl&versionId=m1", "")
					Expect(rec.Code).To(Equal(http.StatusForbidden), "s3:GetObjectVersion is not s3:GetObjectVersionAcl: %s", rec.Body.String())
				})
				It("answer each read 405 by its own version action", func() {
					policy([3]string{"Allow", "s3:GetObjectVersionAcl", "plain/*"})
					expectMarkerVersion(w.send(w.bob, http.MethodGet, "/plain/k?acl&versionId=m1", ""), http.MethodGet, false)
					for _, target := range []string{"/plain/k?versionId=m1", "/plain/k?tagging&versionId=m1"} {
						rec := w.send(w.bob, http.MethodGet, target, "")
						Expect(rec.Code).To(Equal(http.StatusForbidden), "%s: %s", target, rec.Body.String())
					}
				})
				if rel >= denc.Tentacle {
					It("answer GetObjectAttributes 405 by Tentacle's s3:GetObjectVersionAttributes too", func() {
						policy([3]string{"Allow", "s3:GetObjectVersionAttributes", "plain/*"})
						expectMarkerVersion(w.send(w.bob, http.MethodGet, "/plain/k?attributes&versionId=m1", "", "X-Amz-Object-Attributes", "ETag"), http.MethodGet, false)
					})
				}
				It("answer a missing version 404 NoSuchKey, as before", func() {
					expectMissingVersion(w.send(w.alice, http.MethodGet, "/plain/k?versionId=gone", ""), rel, http.MethodGet)
				})
			})

			Describe("the object ACL and tagging writes", func() {
				// v1 is noncurrent and v2 current, each with its own tag set.
				BeforeEach(func(ctx SpecContext) {
					twoVersions(ctx, w)
					versioned(ctx, meta.BucketVersioned, map[string]string{"k": "v2"})
					for _, v := range []string{"v1", "v2"} {
						set := tags.Set{}
						Expect(set.Add("of", v, tags.MaxObjectTags)).To(Succeed())
						st, err := w.store.StatObject(ctx, w.bucket(ctx), meta.ObjKey{Name: "k", Instance: v})
						Expect(err).NotTo(HaveOccurred())
						Expect(w.store.SetObjectAttrs(ctx, st, map[string][]byte{tags.Attr: encodeTags(set)}, nil)).To(Succeed())
					}
				})
				DescribeTable("change the version named, the current one when none is, and leave the other alone",
					func(ctx SpecContext, method, query, body, attr string, hdr ...string) {
						for _, c := range []struct{ named, written, other string }{{"v1", "v1", "v2"}, {"", "v2", "v1"}} {
							target := "/plain/k?" + query
							if c.named != "" {
								target += "&versionId=" + c.named
							}
							written, other := w.versionAttr(ctx, "k", c.written, attr), w.versionAttr(ctx, "k", c.other, attr)
							rec := w.send(w.alice, method, target, body, hdr...)
							Expect(rec.Code).To(BeNumerically("<", 300), "%s %s: %s", method, target, rec.Body.String())
							Expect(w.versionAttr(ctx, "k", c.written, attr)).NotTo(Equal(written), "%s %s writes %s", method, target, c.written)
							Expect(w.versionAttr(ctx, "k", c.other, attr)).To(Equal(other), "%s %s leaves %s alone", method, target, c.other)
						}
					},
					Entry("PutObjectAcl", http.MethodPut, "acl", "", meta.AttrACL, "x-amz-acl", "public-read", "Content-Length", "0"),
					Entry("PutObjectTagging", http.MethodPut, "tagging",
						"<Tagging><TagSet><Tag><Key>new</Key><Value>1</Value></Tag></TagSet></Tagging>", tags.Attr),
					Entry("DeleteObjectTagging, which radosgw makes on the key's OLH head when no version is named", http.MethodDelete, "tagging", "", tags.Attr),
				)
				It("are served on a bucket with object lock, whose versions' retention they keep", func(ctx SpecContext) {
					w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketObjLockEnabled })
					rec := w.send(w.alice, http.MethodPut, "/plain/k?tagging&versionId=v1", "<Tagging><TagSet></TagSet></Tagging>")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(w.send(w.alice, http.MethodDelete, "/plain/k?tagging", "").Code).To(Equal(http.StatusNoContent))
					Expect(w.versionAttr(ctx, "k", "v2", tags.Attr)).To(BeNil())
				})
			})

			Describe("UploadPartCopy", func() {
				var id string
				BeforeEach(func(ctx SpecContext) {
					twoVersions(ctx, w)
					versioned(ctx, meta.BucketVersioned, map[string]string{"k": "v2"})
					id = w.initUpload(w.alice, "/plain/dst")
				})
				DescribeTable("copies the version its source names, the current one when it names none",
					func(ctx SpecContext, source, data string) {
						rec := w.send(w.alice, http.MethodPut, "/plain/dst?uploadId="+id+"&partNumber=1", "",
							"Content-Length", "0", "X-Amz-Copy-Source", source)
						Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
						Expect(rec.Body.String()).To(ContainSubstring(md5Hex(data)))
						Expect(w.parts(ctx, "dst", id)).To(ConsistOf(And(HaveField("Number", 1), HaveField("ETag", md5Hex(data)))))
					},
					Entry("a noncurrent version", "/plain/k?versionId=v1", "one"),
					Entry("the current version, named", "/plain/k?versionId=v2", "two"),
					Entry("no version", "/plain/k", "two"),
				)
			})

			Describe("a copy source naming a delete marker's version", func() {
				var copies map[string]func(who *op.UserRecord, source string) *httptest.ResponseRecorder
				BeforeEach(func(ctx SpecContext) {
					twoVersions(ctx, w)
					versioned(ctx, meta.BucketVersioned, map[string]string{"k": "v2"}, meta.ObjKey{Name: "k", Instance: "m1"})
					id := w.initUpload(w.alice, "/plain/dst")
					copies = map[string]func(who *op.UserRecord, source string) *httptest.ResponseRecorder{
						"CopyObject": func(who *op.UserRecord, source string) *httptest.ResponseRecorder {
							return w.send(who, http.MethodPut, "/plain/dst", "", "X-Amz-Copy-Source", source)
						},
						"UploadPartCopy": func(who *op.UserRecord, source string) *httptest.ResponseRecorder {
							return w.send(who, http.MethodPut, "/plain/dst?uploadId="+id+"&partNumber=1", "",
								"Content-Length", "0", "X-Amz-Copy-Source", source)
						},
					}
				})
				It("is 400 InvalidRequest to a requester who may read the version, and copies nothing", func(ctx SpecContext) {
					for _, copyFrom := range copies {
						expectError(copyFrom(w.alice, "/plain/k?versionId=m1"), http.StatusBadRequest, "InvalidRequest")
					}
					Expect(w.stat(ctx, "dst").Exists).To(BeFalse())
				})
				DescribeTable("keeps a missing source's answer for a requester who may not read the version",
					func(code int, stmts ...[3]string) {
						rec := w.send(w.alice, http.MethodPut, "/plain?policy", bobPolicy(stmts...))
						Expect(rec.Code).To(Equal(http.StatusNoContent), rec.Body.String())
						for name, copyFrom := range copies {
							rec := copyFrom(w.bob, "/plain/k?versionId=m1")
							Expect(rec.Code).To(Equal(code), "%s: %s", name, rec.Body.String())
						}
					},
					Entry("404 to one who may list the bucket", http.StatusNotFound,
						[3]string{"Allow", "s3:PutObject", "plain/*"}, [3]string{"Allow", "s3:ListBucket", "plain"}),
					Entry("403 to one who may not", http.StatusForbidden, [3]string{"Allow", "s3:PutObject", "plain/*"}),
				)
				It("is 404 NoSuchKey for a source whose current version is a delete marker, named by no version", func(ctx SpecContext) {
					versioned(ctx, 0, map[string]string{"k": "m1"}, meta.ObjKey{Name: "k", Instance: "m1"})
					for name, copyFrom := range copies {
						rec := copyFrom(w.alice, "/plain/k")
						Expect(rec.Code).To(Equal(http.StatusNotFound), "%s: %s", name, rec.Body.String())
					}
				})
			})
		})
	}
})

// withArg is target with arg added to its query.
func withArg(target, arg string) string {
	if strings.Contains(target, "?") {
		return target + "&" + arg
	}
	return target + "?" + arg
}

// twoVersions stores k in plain as v1 ("one") then v2 ("two"), and names v2
// current.
func twoVersions(ctx context.Context, w *writeWorld) map[string]string {
	GinkgoHelper()
	w.putVersion(ctx, "k", "v1", "one")
	w.putVersion(ctx, "k", "v2", "two")
	return map[string]string{"k": "v2"}
}

// nullCurrent stores k's null version ("null") and v1 ("one") in plain, and
// names the null version current.
func nullCurrent(ctx context.Context, w *writeWorld) map[string]string {
	GinkgoHelper()
	w.putVersion(ctx, "k", "v1", "one")
	w.put(ctx, "k", "null", nil)
	return map[string]string{"k": ""}
}

// plainOnly stores k ("plain") in plain, which has no OLH to follow.
func plainOnly(ctx context.Context, w *writeWorld) map[string]string {
	GinkgoHelper()
	w.put(ctx, "k", "plain", nil)
	return nil
}
