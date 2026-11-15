package op_test

import (
	"fmt"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// maxParts is rgw_multipart_part_upload_limit's default and S3's limit.
const maxParts = 10000

var startTag = regexp.MustCompile(`<[A-Za-z]`)

// partsOf is n Part elements, each with its number, a quoted MD5 as its ETag
// and, when checksum is not empty, an element of that name holding a CRC32C's
// base64, which a part of an upload with a checksum algorithm carries.
func partsOf(n int, checksum string) []string {
	parts := make([]string, n)
	for i := range parts {
		extra := ""
		if checksum != "" {
			extra = "<" + checksum + ">AAAAAA==</" + checksum + ">"
		}
		parts[i] = fmt.Sprintf(`<Part><PartNumber>%d</PartNumber><ETag>"%032x"</ETag>%s</Part>`, i+1, i, extra)
	}
	return parts
}

var _ = Describe("ParseCompleteMultipart against the bounds on a document's elements and depth", func() {
	DescribeTable("reads the largest document radosgw's limits allow",
		func(checksum string, n int) {
			doc := completeDoc(partsOf(n, checksum)...)
			Expect(len(doc)).To(BeNumerically("<=", 1<<20), "within rgw_max_put_param_size")
			parts, err := op.ParseCompleteMultipart([]byte(doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(parts).To(HaveLen(n))
			elements := len(startTag.FindAllStringIndex(doc, -1)) + strings.Count(doc, `="`)
			Expect(elements * 3 / 2).To(BeNumerically("<=", xmltext.MaxElements))
		},
		Entry("rgw_multipart_part_upload_limit parts", "", maxParts),
		Entry("parts that each carry a checksum, as many as fit", "ChecksumCRC32C", 7600),
	)

	// Each document holds a Part, so without the bound it parses and the
	// refusal is the bound's.
	It("answers MalformedXML for a document past the bound, as it does for one that does not parse", func() {
		_, err := op.ParseCompleteMultipart([]byte(completeDoc(xmlPart("1", "x") + strings.Repeat("<x/>", xmltext.MaxElements))))
		Expect(err).To(MatchError(op.ErrMalformedXML))

		nest := strings.Repeat("<x>", xmltext.MaxDepth) + strings.Repeat("</x>", xmltext.MaxDepth)
		_, err = op.ParseCompleteMultipart([]byte(completeDoc(xmlPart("1", "x") + nest)))
		Expect(err).To(MatchError(op.ErrMalformedXML))
	})
})
