package citecheck_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

var _ = Describe("ScopeOf", func() {
	DescribeTable("a block header that opens a scope, and the names it gives",
		func(header string, names []string) {
			scope, got := citecheck.ScopeOf(header)
			Expect(scope).To(BeTrue(), "header %q", header)
			Expect(got).To(Equal(names), "header %q", header)
		},
		Entry("a qualified namespace", "namespace rgw::sal", []string{"rgw", "sal"}),
		Entry("an anonymous namespace", "namespace", nil),
		Entry("an inline namespace", "inline namespace v1", nil),
		Entry(`an extern "C" block, its string blanked`, `extern    `, nil),
		Entry("a class", "class RGWRados", []string{"RGWRados"}),
		Entry("a class template with a base", "template <typename T>\nclass Foo : public Bar<T>", []string{"Foo"}),
		Entry("a struct after an export macro, final, with two bases",
			"struct CEPH_RGW_API rgw_foo final : public rgw_bar, private baz", []string{"rgw_foo"}),
		Entry("a struct after a macro line with no semicolon", "WRITE_CLASS_ENCODER(rgw_foo)\n\nstruct rgw_bar",
			[]string{"rgw_bar"}),
		Entry("a struct after an access specifier", "public:\n  struct Read", []string{"Read"}),
		Entry("a struct with attributes", "struct [[nodiscard]] alignas(8) Foo", []string{"Foo"}),
		Entry("a specialization, its template arguments dropped", "template <> struct denc_traits<rgw_zone_set>",
			[]string{"denc_traits"}),
		Entry("a nested class defined out of line", "class RGWRados::Object::Stat", []string{"RGWRados", "Object", "Stat"}),
		Entry("an anonymous struct", "struct", nil),
	)

	DescribeTable("a block header that opens no scope",
		func(header string) {
			scope, _ := citecheck.ScopeOf(header)
			Expect(scope).To(BeFalse(), "header %q", header)
		},
		Entry("an enum class", "enum class Color"),
		Entry("a function", "int RGWRados::Object::Read::prepare(optional_yield y)"),
		Entry("a function template", "template <class T>\nvoid f(T x)"),
		Entry("a function returning a struct", "struct foo make_foo(int x)"),
		Entry("a function taking a class", "void f(class Foo* p)"),
		Entry("a struct variable's initializer", "struct foo bar ="),
		Entry("a control statement", "if (x)"),
	)
})
