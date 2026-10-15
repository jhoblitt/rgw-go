package citecheck_test

import (
	"os"
	"regexp"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("The list of English words", func() {
	It("holds lower-case words of three letters or more, sorted, once each: the common words, not the code names "+
		"the docs write in prose", func() {
		data, err := os.ReadFile("english.txt")
		Expect(err).NotTo(HaveOccurred())
		var words []string
		for line := range strings.Lines(string(data)) {
			if line = strings.TrimSuffix(line, "\n"); !strings.HasPrefix(line, "#") {
				words = append(words, line)
			}
		}
		Expect(slices.IsSorted(words)).To(BeTrue(), "english.txt is not sorted")
		Expect(slices.Compact(slices.Clone(words))).To(HaveLen(len(words)), "english.txt repeats a word")
		re := regexp.MustCompile(`^[a-z]{3,}$`)
		for _, w := range words {
			Expect(re.MatchString(w)).To(BeTrue(), "english.txt holds %q, which is no lower-case word of three letters", w)
		}
		Expect(words).To(ContainElements("bucket", "owner", "user", "object", "remove"))
		for _, code := range []string{"etag", "reinit", "chown", "ceph", "rgw", "uid", "ofs"} {
			Expect(words).NotTo(ContainElement(code), "english.txt holds %s, which names code", code)
		}
	})
})
