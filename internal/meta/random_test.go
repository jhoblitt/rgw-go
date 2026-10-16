package meta_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// The tables of gen_rand_alphanumeric_plain and gen_rand_numeric.
const (
	plainTable   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	numericTable = "0123456789"
)

var _ = Describe("ChooseFrom", func() {
	// On x86-64 a char is signed, so static_cast<unsigned> takes a byte b of
	// 0x80 or more to 2^32 - 256 + b. 2^32 - 256 is 0 modulo 10, 36 and 64,
	// and 58 modulo 62.
	DescribeTable("reads each byte as choose_from's static_cast<unsigned>(char) does on x86-64",
		func(table string, in []byte, want string) {
			Expect(meta.ChooseBytes(table, in)).To(Equal(want), "bytes % x", in)
		},
		Entry("62, the plain table: a high byte picks four characters before b mod 62",
			plainTable, []byte{0x00, 0x7f, 0x80, 0x84, 0xbe, 0xff}, "ADAEAD"),
		Entry("36, the lower table: a high byte picks b mod 36",
			lowerTable, []byte{0x00, 0x7f, 0x80, 0xff}, "0jk3"),
		Entry("64, the no-underscore table: a high byte picks b mod 64",
			noUnderscoreTable, []byte{0x00, 0x3e, 0x7f, 0x80, 0xff}, "A-.A."),
		Entry("10, the numeric table: a high byte picks b mod 10",
			numericTable, []byte{0x00, 0x7f, 0x80, 0xff}, "0785"),
	)

	It("draws A to D six times in every 256 bytes from the plain table, and each other character four", func() {
		every := make([]byte, 256)
		for i := range every {
			every[i] = byte(i)
		}
		got := meta.ChooseBytes(plainTable, every)
		for i, c := range plainTable {
			want := 4
			if i < 4 {
				want = 6
			}
			Expect(strings.Count(got, string(c))).To(Equal(want), "character %c", c)
		}
	})

	It("draws n characters of the table", func() {
		Expect(meta.ChooseFrom(numericTable, 17)).To(MatchRegexp(`^[0-9]{17}$`))
		Expect(meta.ChooseFrom(numericTable, 0)).To(BeEmpty())
	})
})
