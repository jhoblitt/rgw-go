package s3_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

var startTag = regexp.MustCompile(`<[A-Za-z]`)

// deleteObject is an Object holding every element a multi-object delete can
// name, for key number i.
func deleteObject(i int) string {
	return fmt.Sprintf("<Key>%s%d</Key><VersionId>%032x</VersionId><ETag>%032x</ETag>"+
		"<LastModifiedTime>Mon, 28 Sep 2026 12:00:00 GMT</LastModifiedTime><Size>%d</Size>",
		strings.Repeat("k", 100), i, i, i, i)
}

var _ = Describe("parseDelete against the bounds on a document's elements and depth", func() {
	// rgw_delete_multi_obj_max_num is checked once the document is read, so
	// the most keys a body holds is what 1 MiB of the shortest ones allows.
	DescribeTable("reads the largest document radosgw's limits allow",
		func(object func(int) string, n int) {
			objects := make([]string, n)
			for i := range objects {
				objects[i] = object(i)
			}
			doc := deleteDoc(objects...)
			Expect(len(doc)).To(BeNumerically("<=", 1<<20), "within rgw_max_put_param_size")
			for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
				entries, _, err := s3.ParseDeleteForTest([]byte(doc), rel)
				Expect(err).NotTo(HaveOccurred(), rel.String())
				Expect(entries).To(HaveLen(n), rel.String())
			}
			elements := len(startTag.FindAllStringIndex(doc, -1)) + strings.Count(doc, `="`)
			Expect(elements * 3 / 2).To(BeNumerically("<=", xmltext.MaxElements))
		},
		Entry("rgw_delete_multi_obj_max_num keys, each with every element", deleteObject, 1000),
		Entry("the most keys 1 MiB holds", func(i int) string { return fmt.Sprintf("<Key>%d</Key>", i) }, 31000),
	)

	It("answers its own error for a document past the bound, as it does for one that does not parse", func() {
		const object = "<Object><Key>k</Key></Object>"
		past := "<Delete>" + object + strings.Repeat("<x/>", xmltext.MaxElements) + "</Delete>"
		nested := "<Delete>" + object + strings.Repeat("<x>", xmltext.MaxDepth) + strings.Repeat("</x>", xmltext.MaxDepth) + "</Delete>"
		for _, doc := range []string{past, nested} {
			_, _, err := s3.ParseDeleteForTest([]byte(doc), denc.Squid)
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			_, _, err = s3.ParseDeleteForTest([]byte(doc), denc.Tentacle)
			Expect(err).To(MatchError(op.ErrMalformedXML))
			Expect(op.AsError(err).Message).To(Equal("Failed to parse xml input"))
		}
	})
})

var _ = Describe("PUT bucket against the bounds on a document's elements and depth", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		It("answers InvalidArgument on "+rel.String()+" for a CreateBucketConfiguration past the bound, as it does for one that does not parse", func(ctx SpecContext) {
			w := newWriteWorld(ctx, rel)
			const head = "<CreateBucketConfiguration><LocationConstraint></LocationConstraint>"
			for _, body := range []string{
				head + strings.Repeat("<x/>", xmltext.MaxElements) + "</CreateBucketConfiguration>",
				head + strings.Repeat("<x>", xmltext.MaxDepth) + strings.Repeat("</x>", xmltext.MaxDepth) + "</CreateBucketConfiguration>",
				head,
			} {
				rec := w.send(w.alice, http.MethodPut, "/fresh", body)
				Expect(rec.Code).To(Equal(400))
				Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"))
			}
		})
	}
})

var _ = Describe("PUT bucket versioning against the bounds on a document's elements and depth", func() {
	// Its table reads any element it does not place as a field, so the scan
	// admits a flat run of them and the tree is the first to count.
	It("answers MalformedXML to a body past the bound, as it does to one that does not parse", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Squid)
		for _, body := range []string{
			`<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status>` +
				strings.Repeat("<x/>", xmltext.MaxElements) + `</VersioningConfiguration>`,
			`<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status>`,
		} {
			rec := w.send(w.alice, http.MethodPut, "/plain?versioning", body)
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>MalformedXML</Code>"))
		}
	})
})
