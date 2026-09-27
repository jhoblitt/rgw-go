package gate_test

import (
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// multipartUpload is what tells a multipart object's part heads from an
// earlier upload of the same key apart from anything else: the part heads'
// common name prefix, the upload ids the current part heads carry, and the
// part count.
type multipartUpload struct {
	prefix  string
	current map[string]bool
	parts   int
}

// newMultipartUpload describes key's upload in the bucket with marker from
// its part count and current part heads, each named
// "<marker>__multipart_<key>.<upload id>.<part number>".
func newMultipartUpload(marker, key string, parts int, heads []string) multipartUpload {
	GinkgoHelper()
	u := multipartUpload{
		prefix:  marker + "_" + meta.ObjKey{Name: key + ".", NS: meta.NSMultipart}.OID(),
		current: map[string]bool{},
		parts:   parts,
	}
	for _, h := range heads {
		id, _, ok := u.split(h)
		Expect(ok).To(BeTrue(), "part head %s is not named <prefix><upload id>.<part number>", h)
		u.current[id] = true
	}
	return u
}

// split parses a part head name into its upload id and part number. Neither
// an upload id nor a re-uploaded part's random prefix holds a dot (both come
// from gen_rand_alphanumeric's URL-safe table), so a longer key's part heads
// do not parse.
func (u multipartUpload) split(oid string) (string, int, bool) {
	rest, ok := strings.CutPrefix(oid, u.prefix)
	if !ok {
		return "", 0, false
	}
	id, part, ok := strings.Cut(rest, ".")
	if !ok || id == "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(part)
	return id, n, err == nil
}

// staleNames reports whether oid names a part head of an earlier upload:
// another upload id than the current part heads carry, and a part number
// the upload could have had. A stray object of the current upload does not
// pass.
func (u multipartUpload) staleNames(oid string) bool {
	id, n, ok := u.split(oid)
	return ok && !u.current[id] && n >= 1 && n <= u.parts
}

var _ = Describe("the stale part-head allowance", func() {
	const marker = "zone.4156.1"
	var u multipartUpload

	BeforeEach(func() {
		u = newMultipartUpload(marker, "multipart.bin", 3, []string{
			marker + "__multipart_multipart.bin.2~cur.1",
			marker + "__multipart_multipart.bin.2~cur.2",
			marker + "__multipart_multipart.bin.reup.3", // a re-uploaded part's override prefix
		})
	})

	DescribeTable("names a part head of an earlier upload",
		func(oid string) { Expect(u.staleNames(oid)).To(BeTrue(), oid) },
		Entry("the first part", marker+"__multipart_multipart.bin.2~old.1"),
		Entry("the last part", marker+"__multipart_multipart.bin.2~old.3"),
	)

	DescribeTable("names nothing else",
		func(oid string) { Expect(u.staleNames(oid)).To(BeFalse(), oid) },
		Entry("a stray part head of the current upload", marker+"__multipart_multipart.bin.2~cur.3"),
		Entry("a stray under a current re-upload prefix", marker+"__multipart_multipart.bin.reup.1"),
		Entry("a part number past the part count", marker+"__multipart_multipart.bin.2~old.4"),
		Entry("part number 0", marker+"__multipart_multipart.bin.2~old.0"),
		Entry("no part number", marker+"__multipart_multipart.bin.2~old"),
		Entry("another key sharing the prefix", marker+"__multipart_multipart.bin2.2~old.1"),
		Entry("a longer key's part head", marker+"__multipart_multipart.bin.x.2~old.1"),
		Entry("a shadow stripe", marker+"__shadow_multipart.bin.2~old.1_1"),
		Entry("another bucket", "zone.9.9__multipart_multipart.bin.2~old.1"),
	)
})
