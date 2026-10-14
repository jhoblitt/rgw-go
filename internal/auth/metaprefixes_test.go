package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// stringListOf is the elements of the string list that the package-level
// var name is initialized to in the Go file at path. The tables it reads
// are unexported in packages auth does not import, so their source is the
// only way to them.
func stringListOf(path, name string) []string {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	Expect(err).NotTo(HaveOccurred())
	var out []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != name {
				continue
			}
			Expect(vs.Values).To(HaveLen(1), "%s: %s has one value", path, name)
			lit, ok := vs.Values[0].(*ast.CompositeLit)
			Expect(ok).To(BeTrue(), "%s: %s is a %T, not a composite literal", path, name, vs.Values[0])
			for _, e := range lit.Elts {
				bl, ok := e.(*ast.BasicLit)
				Expect(ok).To(BeTrue(), "%s: an element of %s is a %T, not a literal", path, name, e)
				Expect(bl.Kind).To(Equal(token.STRING), "%s: an element of %s is not a string", path, name)
				s, err := strconv.Unquote(bl.Value)
				Expect(err).NotTo(HaveOccurred())
				out = append(out, s)
			}
		}
	}
	Expect(out).NotTo(BeEmpty(), "%s declares %s", path, name)
	return out
}

var _ = Describe("amzHeaderPrefixes", func() {
	It("is the prefix set the request attributes and the SSE condition keys rename", func() {
		var fromEnv []string
		for _, env := range stringListOf(filepath.Join("..", "s3", "requestattrs.go"), "metaPrefixes") {
			Expect(env).To(HavePrefix("HTTP_"), "an environment name")
			fromEnv = append(fromEnv, asciiLower(strings.ReplaceAll(strings.TrimPrefix(env, "HTTP_"), "_", "-")))
		}
		Expect(amzHeaderPrefixes).To(Equal(fromEnv), "internal/s3 keeps meta_prefixes in its own order")

		var fromAuthz []string
		for _, p := range stringListOf(filepath.Join("..", "authz", "env.go"), "metaPrefixes") {
			fromAuthz = append(fromAuthz, asciiLower(p))
		}
		Expect(amzHeaderPrefixes).To(ConsistOf(fromAuthz), "internal/authz keeps them in the order init_meta_info visits them")
	})
})
