package memstore_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// failingWriter fails every write, as a client that went away does.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// readCopy stats dst's "copy" and checks it holds the copied data.
func readCopy(ctx context.Context, store *memstore.Store, dst *op.BucketRecord) *op.ObjectState {
	GinkgoHelper()
	st, err := store.StatObject(ctx, dst, meta.ObjKey{Name: "copy"})
	Expect(err).NotTo(HaveOccurred())
	var buf bytes.Buffer
	Expect(store.ReadObject(ctx, st, op.ByteRange{Length: st.Size}, &buf)).To(Succeed())
	Expect(buf.String()).To(Equal("data"), "data")
	return st
}

var _ = Describe("objects", func() {
	var (
		store *memstore.Store
		clk   *clock
		rec   *op.BucketRecord
		key   meta.ObjKey
	)
	BeforeEach(func(ctx SpecContext) {
		store, clk = newStore()
		rec = mustCreate(ctx, store, "", "b", owner("alice"))
		key = meta.ObjKey{Name: "k"}
	})

	It("stats a missing key as not existing, without an error", func(ctx SpecContext) {
		st, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(Equal(&op.ObjectState{Bucket: rec, Key: key}))
	})
	It("puts an object under its MD5 and stats it back with the attrs and the etag attr", func(ctx SpecContext) {
		attrs := map[string][]byte{meta.AttrContentType: []byte("text/plain\x00"), meta.AttrMetaPrefix + "color": []byte("blue\x00")}
		res, err := store.PutObject(ctx, rec, key, strings.NewReader("hello"), op.PutParams{Attrs: attrs, Size: 5})
		Expect(err).NotTo(HaveOccurred())
		etag := md5Hex([]byte("hello"))
		Expect(res.ETag).To(Equal(etag), "etag")
		Expect(res.Size).To(BeEquivalentTo(5), "size")
		Expect(res.Mtime).To(Equal(start), "mtime")
		Expect(res.Epoch).NotTo(BeZero(), "epoch")
		st, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.WriteTag).NotTo(BeEmpty(), "write tag")
		Expect(st).To(Equal(&op.ObjectState{
			Bucket: rec,
			Key:    key,
			Exists: true,
			Size:   5,
			Mtime:  start,
			Epoch:  res.Epoch,
			Attrs: map[string][]byte{
				meta.AttrContentType:          []byte("text/plain\x00"),
				meta.AttrMetaPrefix + "color": []byte("blue\x00"),
				meta.AttrETag:                 []byte(etag),
			},
			ETag:        etag,
			WriteTag:    st.WriteTag,
			ContentType: "text/plain",
		}))
	})
	It("stores a given ETag, mtime and storage class instead of its own", func(ctx SpecContext) {
		mtime := start.Add(-time.Hour)
		res, err := store.PutObject(ctx, rec, key, strings.NewReader("x"), op.PutParams{Size: 1, ETag: "abc-2", Mtime: mtime, StorageClass: "COLD"})
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{res.ETag, res.Mtime}).To(Equal([]any{"abc-2", mtime}))
		st, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{st.ETag, string(st.Attrs[meta.AttrETag]), st.StorageClass}).To(Equal([]any{"abc-2", "abc-2", "COLD"}))
	})
	It("reads a body of unknown size whole", func(ctx SpecContext) {
		res, err := store.PutObject(ctx, rec, key, strings.NewReader("hello"), op.PutParams{Size: -1})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Size).To(BeEquivalentTo(5))
	})
	It("refuses a body longer than declared with EntityTooLarge", func(ctx SpecContext) {
		_, err := store.PutObject(ctx, rec, key, strings.NewReader("hello"), op.PutParams{Size: 4})
		Expect(err).To(MatchError(op.ErrEntityTooLarge))
		st, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeFalse(), "nothing stored")
	})
	It("refuses a body shorter than declared with RequestTimeout, as RGWPutObj does", func(ctx SpecContext) {
		_, err := store.PutObject(ctx, rec, key, strings.NewReader("hello"), op.PutParams{Size: 6})
		Expect(err).To(MatchError(op.ErrRequestTimeout))
	})
	It("prefetches the first 4 MiB of the head, which StatObject leaves out", func(ctx SpecContext) {
		data := bytes.Repeat([]byte("0123456789abcdef"), (5<<20)/16)
		_, err := store.PutObject(ctx, rec, key, bytes.NewReader(data), op.PutParams{Size: int64(len(data))})
		Expect(err).NotTo(HaveOccurred())
		st, err := store.PrefetchObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Size).To(BeEquivalentTo(5<<20), "the state is StatObject's")
		Expect(st.Head).To(Equal(data[:4<<20]))
		st.Head[0] = 'x'
		st, err = store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Head).To(BeNil())
		var buf bytes.Buffer
		Expect(store.ReadObject(ctx, st, op.ByteRange{Length: 1}, &buf)).To(Succeed())
		Expect(buf.String()).To(Equal("0"), "the prefetched head is a copy")
	})
	It("prefetches a head shorter than 4 MiB whole, and a missing key as not existing", func(ctx SpecContext) {
		mustPut(ctx, store, rec, "k", "hello")
		st, err := store.PrefetchObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Head).To(Equal([]byte("hello")))
		st, err = store.PrefetchObject(ctx, rec, meta.ObjKey{Name: "missing"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(Equal(&op.ObjectState{Bucket: rec, Key: meta.ObjKey{Name: "missing"}}))
	})
	It("gives each write a fresh epoch", func(ctx SpecContext) {
		first := mustPut(ctx, store, rec, "k", "a")
		second := mustPut(ctx, store, rec, "k", "b")
		Expect(second.Epoch).To(BeNumerically(">", first.Epoch))
	})
	DescribeTable("checks If-Match and If-None-Match as prepare_atomic_modification does",
		func(ctx SpecContext, exists bool, ifMatch, ifNoneMatch string, want error) {
			if exists {
				mustPut(ctx, store, rec, "k", "old")
			}
			_, err := store.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Size: 3, IfMatch: ifMatch, IfNoneMatch: ifNoneMatch})
			if want == nil {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(want))
			}
		},
		Entry("If-Match * on an object", true, "*", "", nil),
		Entry("If-Match * on no object", false, "*", "", op.ErrPreconditionFailed),
		Entry("If-Match of the ETag", true, md5Hex([]byte("old")), "", nil),
		Entry("If-Match of another ETag", true, md5Hex([]byte("other")), "", op.ErrPreconditionFailed),
		Entry("If-Match of an ETag on no object", false, md5Hex([]byte("old")), "", op.ErrPreconditionFailed),
		Entry("If-None-Match * on no object", false, "", "*", nil),
		Entry("If-None-Match * on an object", true, "", "*", op.ErrPreconditionFailed),
		Entry("If-None-Match of the ETag", true, "", md5Hex([]byte("old")), op.ErrPreconditionFailed),
		Entry("If-None-Match of another ETag", true, "", md5Hex([]byte("other")), nil),
		Entry("If-None-Match of an ETag on no object, which has no ETag to compare", false, "", md5Hex([]byte("old")), op.ErrPreconditionFailed),
	)
	It("reads a range, clamping its end to the object", func(ctx SpecContext) {
		mustPut(ctx, store, rec, "k", "hello world")
		st, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		for _, c := range []struct {
			rng  op.ByteRange
			want string
		}{
			{op.ByteRange{Offset: 0, Length: 11}, "hello world"},
			{op.ByteRange{Offset: 6, Length: 3}, "wor"},
			{op.ByteRange{Offset: 6, Length: 100}, "world"},
			{op.ByteRange{Offset: 11, Length: 1}, ""},
		} {
			var buf bytes.Buffer
			Expect(store.ReadObject(ctx, st, c.rng, &buf)).To(Succeed(), "%+v", c.rng)
			Expect(buf.String()).To(Equal(c.want), "%+v", c.rng)
		}
	})
	It("refuses a range starting past the end with InvalidRange", func(ctx SpecContext) {
		mustPut(ctx, store, rec, "k", "hello")
		st, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.ReadObject(ctx, st, op.ByteRange{Offset: 6, Length: 1}, &bytes.Buffer{})).To(MatchError(op.ErrInvalidRange))
	})
	It("passes a sink's write error through", func(ctx SpecContext) {
		mustPut(ctx, store, rec, "k", "hello")
		st, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.ReadObject(ctx, st, op.ByteRange{Length: 5}, failingWriter{})).To(MatchError(ContainSubstring("broken pipe")))
	})
	It("reports reading or deleting a missing object as NoSuchKey", func(ctx SpecContext) {
		Expect(store.ReadObject(ctx, &op.ObjectState{Bucket: rec, Key: key}, op.ByteRange{Length: 1}, &bytes.Buffer{})).To(MatchError(op.ErrNoSuchKey), "read")
		Expect(store.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(MatchError(op.ErrNoSuchKey), "delete")
	})
	It("deletes an object, checking If-Match as Tentacle's check_preconditions does", func(ctx SpecContext) {
		res := mustPut(ctx, store, rec, "k", "v")
		Expect(store.DeleteObject(ctx, rec, key, op.DeleteParams{IfMatch: md5Hex([]byte("other"))})).To(MatchError(op.ErrPreconditionFailed), "another ETag")
		Expect(store.DeleteObject(ctx, rec, key, op.DeleteParams{IfMatch: `"` + res.ETag + `"`})).To(Succeed(), "its quoted ETag")
		st, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeFalse(), "deleted")
		mustPut(ctx, store, rec, "k", "v")
		Expect(store.DeleteObject(ctx, rec, key, op.DeleteParams{IfMatch: "*"})).To(Succeed(), "any object")
	})
	It("takes the null instance for the plain object", func(ctx SpecContext) {
		mustPut(ctx, store, rec, "k", "v")
		st, err := store.StatObject(ctx, rec, meta.ObjKey{Name: "k", Instance: "null"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue())
	})
	Context("CopyObject", func() {
		var (
			src *op.ObjectState
			dst *op.BucketRecord
		)
		BeforeEach(func(ctx SpecContext) {
			_, err := store.PutObject(ctx, rec, key, strings.NewReader("data"), op.PutParams{
				Size: 4, ETag: "src-etag", StorageClass: "COLD",
				Attrs: map[string][]byte{"a": []byte("1"), "b": []byte("2")},
			})
			Expect(err).NotTo(HaveOccurred())
			src, err = store.StatObject(ctx, rec, key)
			Expect(err).NotTo(HaveOccurred())
			dst = mustCreate(ctx, store, "", "dst", owner("alice"))
			clk.t = start.Add(time.Minute)
		})
		It("copies the data, merges the attrs over the source's and keeps its ETag", func(ctx SpecContext) {
			res, err := store.CopyObject(ctx, src, dst, meta.ObjKey{Name: "copy"}, op.CopyParams{Attrs: map[string][]byte{"b": []byte("x"), "c": []byte("3")}})
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{res.ETag, res.Size, res.Mtime}).To(Equal([]any{"src-etag", uint64(4), clk.t}))
			Expect(res.Epoch).To(BeNumerically(">", src.Epoch), "a fresh epoch")
			st := readCopy(ctx, store, dst)
			Expect(st.Attrs).To(Equal(map[string][]byte{"a": []byte("1"), "b": []byte("x"), "c": []byte("3"), meta.AttrETag: []byte("src-etag")}), "attrs")
			Expect(st.StorageClass).To(Equal("COLD"), "the source's storage class")
		})
		It("replaces the attrs when asked, still carrying the source's ETag", func(ctx SpecContext) {
			_, err := store.CopyObject(ctx, src, dst, meta.ObjKey{Name: "copy"}, op.CopyParams{Attrs: map[string][]byte{"c": []byte("3")}, ReplaceAttrs: true, StorageClass: "WARM"})
			Expect(err).NotTo(HaveOccurred())
			st := readCopy(ctx, store, dst)
			Expect(st.Attrs).To(Equal(map[string][]byte{"c": []byte("3"), meta.AttrETag: []byte("src-etag")}), "attrs")
			Expect(st.StorageClass).To(Equal("WARM"), "the requested storage class")
		})
		DescribeTable("checks the copy source's conditions as the source read does, unquoting them",
			func(ctx SpecContext, ifMatch, ifNoneMatch string, want error) {
				_, err := store.CopyObject(ctx, src, dst, meta.ObjKey{Name: "copy"}, op.CopyParams{IfMatch: ifMatch, IfNoneMatch: ifNoneMatch})
				if want == nil {
					Expect(err).NotTo(HaveOccurred())
				} else {
					Expect(err).To(MatchError(want))
				}
			},
			Entry("if-match of the quoted ETag", `"src-etag"`, "", nil),
			Entry("if-match of another ETag", `"other"`, "", op.ErrPreconditionFailed),
			Entry("if-none-match of the ETag", "", "src-etag", op.ErrNotModified),
			Entry("if-none-match of another ETag", "", `"other"`, nil),
		)
		It("reports a missing source as NoSuchKey", func(ctx SpecContext) {
			Expect(store.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(Succeed())
			_, err := store.CopyObject(ctx, src, dst, meta.ObjKey{Name: "copy"}, op.CopyParams{})
			Expect(err).To(MatchError(op.ErrNoSuchKey))
		})
	})
	It("sets attrs, then removes the listed ones", func(ctx SpecContext) {
		_, err := store.PutObject(ctx, rec, key, strings.NewReader("v"), op.PutParams{Size: 1, Attrs: map[string][]byte{"a": []byte("1"), "b": []byte("2")}})
		Expect(err).NotTo(HaveOccurred())
		st, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.SetObjectAttrs(ctx, st, map[string][]byte{"b": []byte("x"), "c": []byte("3")}, []string{"a", "c"})).To(Succeed())
		got, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Attrs).To(Equal(map[string][]byte{"b": []byte("x"), meta.AttrETag: []byte(md5Hex([]byte("v")))}))
		Expect(store.SetObjectAttrs(ctx, &op.ObjectState{Bucket: rec, Key: meta.ObjKey{Name: "nope"}}, nil, nil)).To(MatchError(op.ErrNoSuchKey), "missing")
	})
	It("serves concurrent writers, readers and listers", func(ctx SpecContext) {
		const writers = 8
		var wg sync.WaitGroup
		for w := range writers {
			wg.Go(func() {
				defer GinkgoRecover()
				k := meta.ObjKey{Name: "w" + strconv.Itoa(w)}
				_, err := store.PutObject(ctx, rec, k, strings.NewReader("data"), op.PutParams{Size: 4})
				Expect(err).NotTo(HaveOccurred())
				st, err := store.StatObject(ctx, rec, k)
				Expect(err).NotTo(HaveOccurred())
				Expect(store.ReadObject(ctx, st, op.ByteRange{Length: 4}, &bytes.Buffer{})).To(Succeed())
				_, err = store.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: writers})
				Expect(err).NotTo(HaveOccurred())
			})
		}
		wg.Wait()
		res, err := store.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: writers + 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Entries).To(HaveLen(writers))
	})
	It("hands out copies the caller may change freely", func(ctx SpecContext) {
		_, err := store.PutObject(ctx, rec, key, strings.NewReader("v"), op.PutParams{Size: 1, Attrs: map[string][]byte{"a": []byte("1")}})
		Expect(err).NotTo(HaveOccurred())
		st, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		st.Attrs["a"][0] = 'x'
		got, err := store.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Attrs["a"]).To(Equal([]byte("1")))
	})
})
