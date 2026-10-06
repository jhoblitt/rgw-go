package op_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("SortCompleteParts", func() {
	It("sorts by number and keeps the last ETag of a repeated number, as std::map<int, string> does", func() {
		got := op.SortCompleteParts([]op.CompletePart{{Number: 3, ETag: "c"}, {Number: 1, ETag: "a1"}, {Number: 2, ETag: "b"}, {Number: 1, ETag: "a2"}})
		Expect(got).To(Equal([]op.CompletePart{{Number: 1, ETag: "a2"}, {Number: 2, ETag: "b"}, {Number: 3, ETag: "c"}}))
	})
	It("returns no parts for none", func() {
		Expect(op.SortCompleteParts(nil)).To(BeEmpty())
	})
})

var _ = Describe("UploadListing", func() {
	It("lists meta names in the multipart namespace with the request's prefix, delimiter and page", func() {
		lp := op.UploadListing(op.ListUploadsParams{Prefix: "p", Delimiter: "/", MaxUploads: 7})
		Expect([]any{lp.Prefix, lp.Delimiter, lp.MaxKeys, lp.NS, lp.Marker}).To(Equal([]any{"p", "/", 7, meta.NSMultipart, ""}))
		Expect(lp.NameFilter("k.2~x.meta")).To(BeTrue(), "a meta name")
		Expect(lp.NameFilter("k.2~x.1")).To(BeFalse(), "a part head")
		Expect(lp.AllowUnordered).To(BeFalse())
	})
	DescribeTable("builds the marker as RGWMPObj(key_marker, upload_id_marker)'s meta name",
		func(key, id, want string) {
			Expect(op.UploadListing(op.ListUploadsParams{KeyMarker: key, UploadIDMarker: id}).Marker).To(Equal(want))
		},
		Entry("both markers", "k", "2~x", "k.2~x.meta"),
		Entry("a key marker alone, rgw_rest.cc:1646-1655 at v19.2.6", "k", "", "k..meta"),
		Entry("an upload id marker alone, which RGWMPObj clears", "", "2~x", ""),
	)
})

var _ = Describe("UploadsFromListing", func() {
	var rec *op.BucketRecord
	BeforeEach(func() { rec = &op.BucketRecord{} })
	metaEntry := func(name string) op.ObjectEntry {
		return op.ObjectEntry{Key: meta.ObjKey{Name: name, NS: meta.NSMultipart}, Owner: meta.UserOwner(meta.UserID{ID: "alice"}), OwnerDisplayName: "Alice"}
	}
	It("makes each entry an upload of the key and id its meta name holds", func() {
		res := op.UploadsFromListing(rec, op.ListObjectsResult{Entries: []op.ObjectEntry{metaEntry("a.b.2~x.meta")}})
		Expect(res.Uploads).To(HaveLen(1))
		u := res.Uploads[0]
		Expect([]any{u.Key.Name, u.ID, u.OwnerName, u.Bucket}).To(Equal([]any{"a.b", "2~x", "Alice", rec}))
	})
	DescribeTable("names the next markers",
		func(lr op.ListObjectsResult, want [2]string) {
			res := op.UploadsFromListing(rec, lr)
			Expect([2]string{res.NextKeyMarker, res.NextUploadIDMarker}).To(Equal(want))
		},
		Entry("the last upload of a page that is not truncated, prefixes after it or not, rgw_op.cc:6741-6744 at v19.2.6",
			op.ListObjectsResult{Entries: []op.ObjectEntry{metaEntry("a.2~1.meta")}, CommonPrefixes: []string{"b/"}},
			[2]string{"a", "2~1"}),
		Entry("the last upload of a truncated page that ends on it",
			op.ListObjectsResult{Entries: []op.ObjectEntry{metaEntry("c.2~1.meta")}, CommonPrefixes: []string{"b/"}, Truncated: true},
			[2]string{"c", "2~1"}),
		Entry("the common prefix a truncated page ends on, after its last upload",
			op.ListObjectsResult{Entries: []op.ObjectEntry{metaEntry("a.2~1.meta")}, CommonPrefixes: []string{"b/"}, Truncated: true},
			[2]string{"b/", ""}),
		Entry("the last common prefix of a truncated page of prefixes, whatever name the listing read last",
			op.ListObjectsResult{CommonPrefixes: []string{"d1/", "d2/"}, Truncated: true, NextMarker: "e.2~3.07"},
			[2]string{"d2/", ""}),
		Entry("nothing for a truncated page that counted nothing, a zero page",
			op.ListObjectsResult{Truncated: true},
			[2]string{"", ""}),
	)
})
