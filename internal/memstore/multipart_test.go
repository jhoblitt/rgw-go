package memstore_test

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/mptest"
)

// A part at rgw_multipart_min_part_size's default, and a short last part.
var (
	bigPart  = bytes.Repeat([]byte("a"), 5<<20)
	bigETag  = md5Hex(bigPart)
	tailETag = md5Hex([]byte("tail"))
)

// etagOfETags is RadosMultipartUpload::complete's object ETag: the MD5 of the
// parts' binary MD5s, "-" and the part count.
func etagOfETags(etags ...string) string {
	GinkgoHelper()
	h := md5.New()
	for _, e := range etags {
		raw, err := hex.DecodeString(e)
		Expect(err).NotTo(HaveOccurred())
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(etags))
}

func uploadKeys(res op.ListUploadsResult) []string {
	var keys []string
	for _, u := range res.Uploads {
		keys = append(keys, u.Key.Name)
	}
	return keys
}

var _ = Describe("multipart", func() {
	var (
		store *memstore.Store
		rec   *op.BucketRecord
		key   meta.ObjKey
		up    *op.Upload
	)
	BeforeEach(func(ctx SpecContext) {
		store, _ = newStore()
		rec = mustCreate(ctx, store, "", "b", owner("alice"))
		key = meta.ObjKey{Name: "big"}
		var err error
		up, err = store.CreateUpload(ctx, rec, key, op.UploadParams{
			Owner:     owner("alice"),
			OwnerName: "Alice",
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     map[string][]byte{"a": []byte("1"), meta.AttrContentType: []byte("application/x-big\x00")},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("creates an upload under a v2 upload id, as RadosMultipartUpload::init makes one", func(ctx SpecContext) {
		Expect(up.ID).To(MatchRegexp(`^2~[A-Za-z0-9_-]{31}$`), "MULTIPART_UPLOAD_ID_PREFIX and 31 gen_rand_alphanumeric characters")
		Expect(up).To(Equal(&op.Upload{
			ID:        up.ID,
			Bucket:    rec,
			Key:       key,
			Owner:     owner("alice"),
			OwnerName: "Alice",
			Initiated: start,
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     map[string][]byte{"a": []byte("1"), meta.AttrContentType: []byte("application/x-big\x00")},
		}))
		got, err := store.GetUpload(ctx, rec, key, up.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(up), "read back")
		again, err := store.CreateUpload(ctx, rec, key, op.UploadParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(again.ID).NotTo(Equal(up.ID), "a second upload of the key")
	})
	It("reports an unknown upload as NoSuchUpload", func(ctx SpecContext) {
		_, err := store.GetUpload(ctx, rec, key, "2~nope")
		Expect(err).To(MatchError(op.ErrNoSuchUpload), "unknown id")
		_, err = store.GetUpload(ctx, rec, meta.ObjKey{Name: "other"}, up.ID)
		Expect(err).To(MatchError(op.ErrNoSuchUpload), "another key's id")
		Expect(store.Abort(ctx, up)).To(Succeed())
		_, err = store.PutPart(ctx, up, 1, strings.NewReader("x"), op.PutParams{Size: 1})
		Expect(err).To(MatchError(op.ErrNoSuchUpload), "a part of an aborted upload")
		_, err = store.ListParts(ctx, up, 0, 10)
		Expect(err).To(MatchError(op.ErrNoSuchUpload), "listing an aborted upload's parts")
		_, err = store.Complete(ctx, up, []op.CompletePart{{Number: 1}})
		Expect(err).To(MatchError(op.ErrCompletionInProgress), "completing an aborted upload, whose lock fails as the driver's does")
		Expect(op.AsError(err).Message).To(Equal("This multipart completion is already in progress"))
		Expect(err).NotTo(MatchError(op.ErrNoSuchUpload))
		Expect(store.Abort(ctx, up)).To(MatchError(op.ErrNoSuchUpload), "aborting twice")
	})
	It("stores a part under its MD5, replacing one of the same number", func(ctx SpecContext) {
		_, err := store.PutPart(ctx, up, 1, strings.NewReader("aaa"), op.PutParams{Size: 3})
		Expect(err).NotTo(HaveOccurred())
		res, err := store.PutPart(ctx, up, 1, strings.NewReader("bbbb"), op.PutParams{Size: 4})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(&op.PartResult{ETag: md5Hex([]byte("bbbb")), Size: 4, Mtime: start}))
		parts, err := store.ListParts(ctx, up, 0, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(parts.Parts).To(Equal([]op.Part{{Number: 1, ETag: res.ETag, Size: 4, Mtime: start}}))
	})
	It("refuses a part body longer or shorter than declared", func(ctx SpecContext) {
		_, err := store.PutPart(ctx, up, 1, strings.NewReader("aaa"), op.PutParams{Size: 2})
		Expect(err).To(MatchError(op.ErrEntityTooLarge), "longer")
		_, err = store.PutPart(ctx, up, 1, strings.NewReader("aaa"), op.PutParams{Size: 4})
		Expect(err).To(MatchError(op.ErrRequestTimeout), "shorter")
	})
	It("refuses a part whose MD5 is not its Content-MD5 with BadDigest, storing nothing", func(ctx SpecContext) {
		other := md5.Sum([]byte("other"))
		_, err := store.PutPart(ctx, up, 1, strings.NewReader("aaa"), op.PutParams{Size: 3, ContentMD5: other[:]})
		Expect(err).To(MatchError(op.ErrBadDigest))
		parts, err := store.ListParts(ctx, up, 0, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(parts.Parts).To(BeEmpty())
		sum := md5.Sum([]byte("aaa"))
		_, err = store.PutPart(ctx, up, 1, strings.NewReader("aaa"), op.PutParams{Size: 3, ContentMD5: sum[:]})
		Expect(err).NotTo(HaveOccurred())
	})
	It("answers NoSuchUpload for an unknown upload before reading the body", func(ctx SpecContext) {
		body := strings.NewReader("x")
		_, err := store.PutPart(ctx, &op.Upload{ID: "2~nope", Bucket: rec, Key: key}, 1, body, op.PutParams{Size: 1})
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
		Expect(body.Len()).To(Equal(1), "the body is unread")
	})
	It("copies a range of an object into a part", func(ctx SpecContext) {
		mustPut(ctx, store, rec, "src", "hello world")
		src, err := store.StatObject(ctx, rec, meta.ObjKey{Name: "src"})
		Expect(err).NotTo(HaveOccurred())
		res, err := store.CopyPart(ctx, up, 2, src, op.ByteRange{Offset: 6, Length: 5})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(&op.PartResult{ETag: md5Hex([]byte("world")), Size: 5, Mtime: start}))
		_, err = store.CopyPart(ctx, up, 3, src, op.ByteRange{Offset: 12, Length: 1})
		Expect(err).To(MatchError(op.ErrInvalidRange), "a range past the source's end")
	})
	It("lists parts by number after the marker, a page at a time", func(ctx SpecContext) {
		for _, n := range []int{3, 1, 2} {
			_, err := store.PutPart(ctx, up, n, strings.NewReader("x"), op.PutParams{Size: 1})
			Expect(err).NotTo(HaveOccurred())
		}
		numbers := func(r op.ListPartsResult) []int {
			var out []int
			for _, p := range r.Parts {
				out = append(out, p.Number)
			}
			return out
		}
		res, err := store.ListParts(ctx, up, 0, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{numbers(res), res.NextMarker, res.Truncated}).To(Equal([]any{[]int{1, 2}, 2, true}), "first page")
		res, err = store.ListParts(ctx, up, res.NextMarker, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{numbers(res), res.NextMarker, res.Truncated}).To(Equal([]any{[]int{3}, 3, false}), "second page")
		res, err = store.ListParts(ctx, up, res.NextMarker, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{numbers(res), res.NextMarker, res.Truncated}).To(Equal([]any{[]int(nil), 0, false}),
			"an empty page's next marker is 0, where send_response's cur_max starts")
	})
	It("lists uploads in meta-name order, <key>.<upload id>.meta, after radosgw's marker", func(ctx SpecContext) {
		for _, name := range []string{"c", "a", "a"} {
			_, err := store.CreateUpload(ctx, rec, meta.ObjKey{Name: name}, op.UploadParams{})
			Expect(err).NotTo(HaveOccurred())
		}
		all, err := store.ListUploads(ctx, rec, op.ListUploadsParams{MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(uploadKeys(all)).To(Equal([]string{"a", "a", "big", "c"}), "by key")
		ids := []string{all.Uploads[0].ID, all.Uploads[1].ID}
		Expect(ids).To(Equal(slices.Sorted(slices.Values(ids))), "then by id")
		Expect(all.Truncated).To(BeFalse(), "one page")
		last := all.Uploads[3]
		Expect([]string{all.NextKeyMarker, all.NextUploadIDMarker}).To(Equal([]string{"c", last.ID}),
			"a page that is not truncated still names its last upload, as RGWListBucketMultiparts::execute does")

		res, err := store.ListUploads(ctx, rec, op.ListUploadsParams{KeyMarker: "a", UploadIDMarker: ids[0], MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(uploadKeys(res)).To(Equal([]string{"a", "big", "c"}), "after a.<first id>.meta")
		Expect(res.Uploads[0].ID).To(Equal(ids[1]), "the other upload of a")

		res, err = store.ListUploads(ctx, rec, op.ListUploadsParams{KeyMarker: "a", MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(uploadKeys(res)).To(Equal([]string{"a", "a", "big", "c"}),
			"a key marker without an upload id marker is a..meta, which sorts before a's own uploads")

		res, err = store.ListUploads(ctx, rec, op.ListUploadsParams{MaxUploads: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Truncated).To(BeTrue(), "truncated")
		Expect([]string{res.NextKeyMarker, res.NextUploadIDMarker}).To(Equal([]string{"a", ids[1]}), "next markers, the last upload returned")
	})
	It("lists a-b before a, as '-' sorts before the '.' that ends a's key in its meta name", func(ctx SpecContext) {
		for _, name := range []string{"a", "a-b"} {
			_, err := store.CreateUpload(ctx, rec, meta.ObjKey{Name: name}, op.UploadParams{})
			Expect(err).NotTo(HaveOccurred())
		}
		res, err := store.ListUploads(ctx, rec, op.ListUploadsParams{MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(uploadKeys(res)).To(Equal([]string{"a-b", "a", "big"}))
	})
	It("rolls meta names under a delimiter into common prefixes", func(ctx SpecContext) {
		for _, name := range []string{"dir/x", "dir/y", "top"} {
			_, err := store.CreateUpload(ctx, rec, meta.ObjKey{Name: name}, op.UploadParams{})
			Expect(err).NotTo(HaveOccurred())
		}
		res, err := store.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: "/", MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(uploadKeys(res)).To(Equal([]string{"big", "top"}), "uploads")
		Expect(res.CommonPrefixes).To(Equal([]string{"dir/"}), "prefixes")
		res, err = store.ListUploads(ctx, rec, op.ListUploadsParams{Prefix: "dir/", MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(uploadKeys(res)).To(Equal([]string{"dir/x", "dir/y"}), "under the prefix")
		res, err = store.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: ".", MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.CommonPrefixes).To(Equal([]string{"big.", "dir/x.", "dir/y.", "top."}),
			"the delimiter is searched in the whole meta name, as radosgw lists it")
		Expect([]any{res.Uploads, res.NextKeyMarker, res.NextUploadIDMarker}).To(Equal([]any{[]op.Upload(nil), "", ""}),
			"a page of prefixes alone names no upload")
		res, err = store.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: ".", MaxUploads: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{res.CommonPrefixes, res.Truncated}).To(Equal([]any{[]string{"big.", "dir/x."}, true}), "a truncated page of prefixes")
		Expect([]string{res.NextKeyMarker, res.NextUploadIDMarker}).To(Equal([]string{"dir/x.", ""}),
			"a page that ends on a common prefix names it as the key marker")
		res, err = store.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: ".", KeyMarker: res.NextKeyMarker, MaxUploads: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{res.CommonPrefixes, res.Truncated}).To(Equal([]any{[]string{"dir/y.", "top."}, false}), "the page after it")
	})

	It("gives every upload and common prefix once at every page size", func(ctx SpecContext) {
		r := rand.New(rand.NewPCG(uint64(GinkgoRandomSeed()), 5)) //nolint:gosec // a spec's reproducible data
		var keys []string
		for range 12 {
			b := make([]byte, 1+r.IntN(4))
			for i := range b {
				b[i] = "ab/.-x"[r.IntN(6)]
			}
			keys = append(keys, string(b))
			_, err := store.CreateUpload(ctx, rec, meta.ObjKey{Name: string(b)}, op.UploadParams{})
			Expect(err).NotTo(HaveOccurred())
		}
		for _, delim := range []string{"", "/", ".", "-", "x"} {
			for _, prefix := range []string{"", "a"} {
				Expect(mptest.CheckPaging(ctx, store, rec, op.ListUploadsParams{Prefix: prefix, Delimiter: delim})).To(Succeed(),
					"seed %d, keys %q, prefix %q, delimiter %q", GinkgoRandomSeed(), keys, prefix, delim)
			}
		}
	})

	Context("Complete", func() {
		BeforeEach(func(ctx SpecContext) {
			_, err := store.PutPart(ctx, up, 1, bytes.NewReader(bigPart), op.PutParams{Size: int64(len(bigPart))})
			Expect(err).NotTo(HaveOccurred())
			_, err = store.PutPart(ctx, up, 2, strings.NewReader("tail"), op.PutParams{Size: 4})
			Expect(err).NotTo(HaveOccurred())
		})
		It("assembles the parts under the ETag of ETags with the upload's attrs, and removes the upload", func(ctx SpecContext) {
			res, err := store.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: `"` + bigETag + `"`}, {Number: 2, ETag: tailETag}})
			Expect(err).NotTo(HaveOccurred())
			etag := etagOfETags(bigETag, tailETag)
			Expect([]any{res.ETag, res.Size}).To(Equal([]any{etag, uint64(len(bigPart) + 4)}))
			st, err := store.StatObject(ctx, rec, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Attrs).To(Equal(map[string][]byte{
				"a":                  []byte("1"),
				meta.AttrContentType: []byte("application/x-big\x00"),
				meta.AttrETag:        []byte(etag),
			}), "the upload's attrs")
			Expect(st.ContentType).To(Equal("application/x-big"), "content type")
			var buf bytes.Buffer
			Expect(store.ReadObject(ctx, st, op.ByteRange{Length: st.Size}, &buf)).To(Succeed())
			Expect(buf.Bytes()).To(Equal(append(bytes.Clone(bigPart), "tail"...)), "the parts in order")
			_, err = store.GetUpload(ctx, rec, key, up.ID)
			Expect(err).To(MatchError(op.ErrNoSuchUpload), "the upload is gone")
		})
		DescribeTable("refuses a list that does not match the uploaded parts, as RadosMultipartUpload::complete checks them",
			func(ctx SpecContext, parts []op.CompletePart, want error) {
				_, err := store.Complete(ctx, up, parts)
				Expect(err).To(MatchError(want))
				_, err = store.GetUpload(ctx, rec, key, up.ID)
				Expect(err).NotTo(HaveOccurred(), "the upload survives")
			},
			Entry("a repeated part whose last entry has the wrong ETag", []op.CompletePart{{Number: 1, ETag: bigETag}, {Number: 1, ETag: tailETag}, {Number: 2, ETag: tailETag}}, op.ErrInvalidPart),
			Entry("an ETag that does not match", []op.CompletePart{{Number: 1, ETag: bigETag}, {Number: 2, ETag: bigETag}}, op.ErrInvalidPart),
			Entry("a part never uploaded", []op.CompletePart{{Number: 1, ETag: bigETag}, {Number: 3, ETag: tailETag}}, op.ErrInvalidPart),
			Entry("only some of the uploaded parts", []op.CompletePart{{Number: 1, ETag: bigETag}}, op.ErrInvalidPart),
		)
		It("sorts the parts by number and keeps the last of a repeated number, as radosgw's std::map does", func(ctx SpecContext) {
			_, err := store.PutPart(ctx, up, 3, strings.NewReader("more"), op.PutParams{Size: 4})
			Expect(err).NotTo(HaveOccurred())
			_, err = store.PutPart(ctx, up, 2, bytes.NewReader(bigPart), op.PutParams{Size: int64(len(bigPart))})
			Expect(err).NotTo(HaveOccurred())
			res, err := store.Complete(ctx, up, []op.CompletePart{
				{Number: 3, ETag: md5Hex([]byte("more"))}, {Number: 2, ETag: `"bogus"`}, {Number: 1, ETag: bigETag}, {Number: 2, ETag: bigETag},
			})
			Expect(err).NotTo(HaveOccurred(), "rgw_multi.cc:44-53")
			Expect(res.ETag).To(Equal(etagOfETags(bigETag, bigETag, md5Hex([]byte("more")))))
		})
		It("keeps the request's write tag and honors its conditions, leaving a refused upload in place", func(ctx SpecContext) {
			_, err := store.PutObject(ctx, rec, key, strings.NewReader("old"), op.PutParams{Size: 3})
			Expect(err).NotTo(HaveOccurred())
			parts := []op.CompletePart{{Number: 1, ETag: bigETag}, {Number: 2, ETag: tailETag}}
			up.IfNoneMatch = "*"
			_, err = store.Complete(ctx, up, parts)
			Expect(err).To(MatchError(op.ErrPreconditionFailed))
			_, err = store.GetUpload(ctx, rec, key, up.ID)
			Expect(err).NotTo(HaveOccurred(), "the upload survives a refused completion")
			up.IfNoneMatch, up.WriteTag = "", "tx-complete"
			_, err = store.Complete(ctx, up, parts)
			Expect(err).NotTo(HaveOccurred())
			st, err := store.StatObject(ctx, rec, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.WriteTag).To(Equal("tx-complete"))
		})
		It("refuses a part below 5 MiB anywhere but last with EntityTooSmall", func(ctx SpecContext) {
			_, err := store.PutPart(ctx, up, 1, strings.NewReader("small"), op.PutParams{Size: 5})
			Expect(err).NotTo(HaveOccurred())
			_, err = store.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: md5Hex([]byte("small"))}, {Number: 2, ETag: tailETag}})
			Expect(err).To(MatchError(op.ErrEntityTooSmall))
		})
		DescribeTable("answers a completion retried after it succeeded as the driver does, per release",
			func(ctx SpecContext, release denc.Release, wantETag string) {
				store = memstore.New(memstore.Config{Release: release, Now: (&clock{t: start}).now})
				rec = mustCreate(ctx, store, "", "b", owner("alice"))
				var err error
				up, err = store.CreateUpload(ctx, rec, key, op.UploadParams{})
				Expect(err).NotTo(HaveOccurred())
				_, err = store.PutPart(ctx, up, 1, bytes.NewReader(bigPart), op.PutParams{Size: int64(len(bigPart))})
				Expect(err).NotTo(HaveOccurred())
				_, err = store.PutPart(ctx, up, 2, strings.NewReader("tail"), op.PutParams{Size: 4})
				Expect(err).NotTo(HaveOccurred())
				parts := []op.CompletePart{{Number: 1, ETag: `"` + bigETag + `"`}, {Number: 2, ETag: tailETag}}
				first, err := store.Complete(ctx, up, parts)
				Expect(err).NotTo(HaveOccurred())
				Expect(first.ETag).To(Equal(etagOfETags(bigETag, tailETag)))

				again, err := store.Complete(ctx, up, parts)
				Expect(err).NotTo(HaveOccurred(), "check_previously_completed finds the key's ETag the parts make")
				if wantETag == "stored" {
					wantETag = first.ETag
				}
				Expect(again).To(Equal(&op.PutResult{ETag: wantETag, Size: first.Size, Mtime: first.Mtime, Epoch: first.Epoch}))

				reversed, err := store.Complete(ctx, up, []op.CompletePart{parts[1], parts[0]})
				Expect(err).NotTo(HaveOccurred(), "the parts are sorted by number before the ETag is recomputed, as std::map orders them")
				Expect(reversed).To(Equal(again))
				repeated, err := store.Complete(ctx, up, []op.CompletePart{parts[0], {Number: 2, ETag: "bogus"}, parts[1]})
				Expect(err).NotTo(HaveOccurred(), "a repeated part keeps its last ETag, as std::map assignment does")
				Expect(repeated).To(Equal(again))

				_, err = store.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: bigETag}})
				Expect(err).To(MatchError(op.ErrCompletionInProgress), "a different part list makes a different ETag")
				Expect(op.AsError(err).Message).To(Equal("This multipart completion is already in progress"))
				_, err = store.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: bigETag}, {Number: 2, ETag: bigETag}})
				Expect(err).To(MatchError(op.ErrCompletionInProgress), "the same count, other ETags")
				_, err = store.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: "bogus"}, {Number: 2, ETag: tailETag}})
				Expect(err).To(MatchError(op.ErrCompletionInProgress), "an ETag that is not an MD5")
				st, err := store.StatObject(ctx, rec, key)
				Expect(err).NotTo(HaveOccurred())
				Expect(st.ETag).To(Equal(first.ETag), "the object is untouched")
			},
			Entry("Squid answers 200 without an ETag (v19.2.6 rgw_op.cc:6438-6441, :6543-6580)", denc.Squid, ""),
			Entry("Tentacle answers 200 with the stored ETag (v20.2.4 rgw_op.cc:7252-7255, :7472)", denc.Tentacle, "stored"),
		)
		It("keeps the 500 for a missing upload when the key holds an object of another ETag", func(ctx SpecContext) {
			mustPut(ctx, store, rec, "big", "old")
			_, err := store.Complete(ctx, &op.Upload{ID: "2~nope", Bucket: rec, Key: key}, []op.CompletePart{{Number: 1, ETag: bigETag}})
			Expect(err).To(MatchError(op.ErrCompletionInProgress))
		})
	})
	It("completes a single small part", func(ctx SpecContext) {
		_, err := store.PutPart(ctx, up, 1, strings.NewReader("x"), op.PutParams{Size: 1})
		Expect(err).NotTo(HaveOccurred())
		res, err := store.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: md5Hex([]byte("x"))}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(etagOfETags(md5Hex([]byte("x")))))
	})
	It("aborts an upload with its parts", func(ctx SpecContext) {
		_, err := store.PutPart(ctx, up, 1, strings.NewReader("x"), op.PutParams{Size: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Abort(ctx, up)).To(Succeed())
		_, err = store.GetUpload(ctx, rec, key, up.ID)
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
		res, err := store.ListUploads(ctx, rec, op.ListUploadsParams{MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Uploads).To(BeEmpty())
	})
})
