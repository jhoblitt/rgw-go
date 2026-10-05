package meta_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("ParseIndexKeyName", func() {
	DescribeTable("parses an index key name as parse_raw_oid does (rgw_obj_types.h:285-310 at v19.2.6 and v20.2.4)",
		func(name string, want meta.ObjKey) {
			got, ok := meta.ParseIndexKeyName(name)
			Expect(ok).To(BeTrue(), "%q parses", name)
			Expect(got).To(Equal(want), "%q", name)
		},
		Entry("a plain name", "foo", meta.ObjKey{Name: "foo"}),
		Entry("the empty name, whose first byte reads as the terminating NUL", "", meta.ObjKey{}),
		Entry("an escaped leading underscore", "__under", meta.ObjKey{Name: "_under"}),
		Entry("a lone escaped underscore", "__", meta.ObjKey{Name: "_"}),
		Entry("a namespaced name", "_multipart_a.2~x.meta", meta.ObjKey{Name: "a.2~x.meta", NS: "multipart"}),
		Entry("a namespaced name holding underscores", "_shadow_a_b", meta.ObjKey{Name: "a_b", NS: "shadow"}),
		Entry("a namespace carrying an instance, parse_ns_field", "_ns:v1_foo", meta.ObjKey{Name: "foo", NS: "ns", Instance: "v1"}),
		Entry("an instance with no namespace", "_:v1_foo", meta.ObjKey{Name: "foo", Instance: "v1"}),
		Entry("a namespaced empty name", "_ns_", meta.ObjKey{NS: "ns"}),
	)
	DescribeTable("refuses a malformed namespaced name",
		func(name string) {
			_, ok := meta.ParseIndexKeyName(name)
			Expect(ok).To(BeFalse(), "%q", name)
		},
		Entry("a lone underscore, shorter than _x_", "_"),
		Entry("two bytes, shorter than _x_", "_a"),
		Entry("no closing underscore", "_abc"),
	)
	It("inverts IndexKeyName", func() {
		for _, k := range []meta.ObjKey{{Name: "plain"}, {Name: "_lead"}, {Name: "x", NS: "multipart"}, {Name: "_y", NS: "shadow"}} {
			got, ok := meta.ParseIndexKeyName(k.IndexKeyName())
			Expect(ok).To(BeTrue(), "%+v", k)
			Expect(got).To(Equal(k))
		}
	})
})
