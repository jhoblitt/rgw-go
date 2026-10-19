package fakerados

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// An area table declared as every area's is, in a file other than cls_rgw.go.
// Its one method is a read, so RGWWriteMethods stays the class's own.
var _ = rgwTable(methodTable{"spec_area_read": {fn: func(*ClassCall) ([]byte, int32) { return []byte("spec area"), 0 }}})

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// isTableType reports whether e spells a method table's type, by its name or
// as the map it is.
func isTableType(e ast.Expr) bool {
	if isIdent(e, "methodTable") {
		return true
	}
	m, ok := e.(*ast.MapType)
	return ok && isIdent(m.Key, "string") && isIdent(m.Value, "classMethod")
}

// tableDecls reads a file's package-level variables: whether one declares a
// table through rgwTable, and where a table is declared any other way. A
// variable of a table's type is such a table, but for rgwClassMethods, the
// merged class init assigns.
func tableDecls(fset *token.FileSet, f *ast.File) (registers bool, bare []string) {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			typed := vs.Type != nil && isTableType(vs.Type) &&
				!slices.ContainsFunc(vs.Names, func(n *ast.Ident) bool { return n.Name == "rgwClassMethods" })
			if typed && len(vs.Values) == 0 {
				bare = append(bare, fset.Position(vs.Pos()).String())
			}
			for _, v := range vs.Values {
				if call, ok := v.(*ast.CallExpr); ok && isIdent(call.Fun, "rgwTable") {
					registers = true
					continue
				}
				if typed {
					bare = append(bare, fset.Position(v.Pos()).String())
					continue
				}
				ast.Inspect(v, func(n ast.Node) bool {
					if lit, ok := n.(*ast.CompositeLit); ok && isTableType(lit.Type) {
						bare = append(bare, fset.Position(lit.Pos()).String())
					}
					return true
				})
			}
		}
	}
	return registers, bare
}

var _ = Describe("RGWClass's method tables", func() {
	It("answers EOPNOTSUPP for a method no table holds, as the OSD refuses a method the class does not register", func(ctx SpecContext) {
		c := New()
		c.RegisterClassMethods("rgw", RGWClass(), RGWMethods, RGWWriteMethods)
		c.Put("index", "", "shard", []byte("d"))
		p, err := c.Pool(ctx, "index", "")
		Expect(err).NotTo(HaveOccurred())
		op := radosclient.NewWriteOp()
		op.Exec("rgw", "no_such_method", nil)
		_, err = p.Write(ctx, "shard", op, radosclient.OpFlagNone)
		// OpInfo::set_from_op refuses the call at op setup, where
		// get_method_flags finds no method of its name
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/osd_op_util.cc#L189-L196 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/osd_op_util.cc#L189-L196 at v20.2.4).
		Expect(err).To(MatchError(radosclient.ErrNotSupported), "OpInfo::set_from_op, osd_op_util.cc:189-196 at v19.2.7 and v20.2.4")
		Expect(c.Object("index", "", "shard").Data).To(Equal([]byte("d")))
		_, rval := RGWClass()(&ClassCall{Method: "no_such_method"})
		Expect(rval).To(Equal(-int32(syscall.EOPNOTSUPP)), "the class itself, for a name a registration lists and no table holds")
	})

	It("serves a table rgwTable declares in another file, merged once every package variable is set", func() {
		out, rval := RGWClass()(&ClassCall{Method: "spec_area_read"})
		Expect(rval).To(BeZero())
		Expect(out).To(Equal([]byte("spec area")))
		Expect(RGWWriteMethods).NotTo(ContainElement("spec_area_read"))
	})

	It("derives the class's write list from the write flags of every registered table", func() {
		tables := append(slices.Clip(rgwTables), methodTable{"spec_area_write": {fn: nopClassMethod, write: true}})
		_, _, writes := assembleClass(tables...)
		Expect(writes).To(ConsistOf(append(slices.Clone(RGWWriteMethods), "spec_area_write")))
	})

	It("lists exactly the merged tables' methods, reads and writes, in name order", func() {
		Expect(RGWMethods).To(Equal(slices.Sorted(maps.Keys(rgwClassMethods))))
		Expect(RGWMethods).To(ContainElements("bucket_list", "spec_area_read"), "a read of cls_rgw.go's table and of another file's")
		Expect(RGWMethods).To(ContainElements(RGWWriteMethods))
		Expect(RGWMethods).To(HaveCap(len(RGWMethods)), "a caller's append copies the list rather than writing into it")
		tables := append(slices.Clip(rgwTables), methodTable{"a_spec_area_read": {fn: nopClassMethod}})
		_, names, _ := assembleClass(tables...)
		Expect(names).To(Equal(append([]string{"a_spec_area_read"}, RGWMethods...)))
	})

	It("declares every rgw method table through rgwTable, so none is left out of RGWClass", func() {
		files, err := filepath.Glob("cls_rgw*.go")
		Expect(err).NotTo(HaveOccurred())
		fset := token.NewFileSet()
		var registering, bare []string
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
			Expect(err).NotTo(HaveOccurred(), name)
			registers, b := tableDecls(fset, f)
			if registers {
				registering = append(registering, name)
			}
			bare = append(bare, b...)
		}
		Expect(registering).To(ContainElements("cls_rgw.go", "cls_rgw_olh.go", "cls_rgw_lc.go"))
		Expect(bare).To(BeEmpty(), "a method table declared without rgwTable is in none of RGWClass, RGWMethods and RGWWriteMethods")
	})
})
