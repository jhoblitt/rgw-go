package fakerados

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func nopClassMethod(*ClassCall) ([]byte, int32) { return nil, 0 }

var _ = Describe("method tables", func() {
	It("derives the write list from every table's write flags, in name order", func() {
		// Enough names that a list in map order is all but never sorted.
		many := methodTable{"read": {fn: nopClassMethod}}
		for name := range strings.FieldsSeq("j e a h c f b i d") {
			many[name] = classMethod{fn: nopClassMethod, write: true}
		}
		t := mergeMethodTables(many, methodTable{}, methodTable{"g": {fn: nopClassMethod, write: true}})
		writes := t.writeMethods()
		Expect(writes).To(Equal(strings.Fields("a b c d e f g h i j")))
		Expect(writes).To(HaveCap(len(writes)), "a caller's append copies the list rather than writing into it")
	})

	It("panics at assembly on a method two tables hold, since a class registers each name once", func() {
		Expect(func() {
			mergeMethodTables(methodTable{"m": {fn: nopClassMethod}}, methodTable{"m": {fn: nopClassMethod, write: true}})
		}).To(PanicWith(`fakerados: class method "m" is in two method tables`))
	})
})
