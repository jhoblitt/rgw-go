package auth

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// nameUse is how a package uses an x-amz- header name it spells.
type nameUse uint8

const (
	// useOther is a table entry, a comparison, a prefix test or a
	// constant's value.
	useOther nameUse = iota
	// useRead is the name argument of a call that reads a request header:
	// a HeaderValue, Get or Values call, an index, or a call of a function
	// that passes its own argument to one of those.
	useRead
	// useWrite is the name argument of a call that sets or removes a
	// header: SetHeader, addHeader, a Set, Add or Del call, or delete.
	useWrite
)

func (u nameUse) String() string { return [...]string{"other", "read", "write"}[u] }

// headerReaders and headerWriters are the callee names that read and write
// a header by the name they are given. A function passing its own argument
// to a reader is a reader too (readerNames).
var (
	headerReaders = []string{"HeaderValue", "Get", "Values"}
	headerWriters = []string{"SetHeader", "addHeader", "Set", "Add", "Del", "delete"}
)

// amzName is an x-amz- header name a package spells, lower-cased: as a
// string literal, a constant expression, a constant a reader is called
// with, or the constant start of a string the code concatenates (prefix).
// An environment name, HTTP_X_AMZ_..., is taken in its header form.
type amzName struct {
	key exemptKey
	pos token.Position
}

// exemptKey says where and how a name is used: the package's directory in
// the module, its top-level declaration, the name, whether it only begins
// the string built, and the use.
type exemptKey struct {
	pkg, decl, name string
	prefix          bool
	use             nameUse
}

func (n amzName) String() string {
	return fmt.Sprintf("%s:%d %s (%s, %s, prefix %t)", n.pos.Filename, n.pos.Line, n.key.name, n.key.decl, n.key.use, n.key.prefix)
}

// pinSource is the Go files outside tests, testdata and generated fakes of
// every package under the module's dirs, by package directory.
type pinSource struct {
	fset  *token.FileSet
	files map[string][]*ast.File
}

func loadPinSource(module string, dirs ...string) pinSource {
	src := pinSource{fset: token.NewFileSet(), files: map[string][]*ast.File{}}
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(module, dir), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				if name := e.Name(); name == "testdata" || strings.HasSuffix(name, "fakes") || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(src.fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			pkg, err := filepath.Rel(module, filepath.Dir(path))
			if err != nil {
				return err
			}
			src.files[filepath.ToSlash(pkg)] = append(src.files[filepath.ToSlash(pkg)], f)
			return nil
		})
		Expect(err).NotTo(HaveOccurred(), "walking %s", dir)
	}
	return src
}

// constants are the string constants and variables the files declare, by
// package directory and name, and by package name and name for a selector.
type constants map[string]string

func (src pinSource) constants() constants {
	c := constants{}
	for changed := true; changed; {
		changed = false
		for pkg, files := range src.files {
			for _, f := range files {
				for _, decl := range f.Decls {
					gen, ok := decl.(*ast.GenDecl)
					if !ok {
						continue
					}
					for _, spec := range gen.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok || len(vs.Values) != len(vs.Names) {
							continue
						}
						for i, name := range vs.Names {
							v, ok, _ := c.fold(pkg, vs.Values[i])
							if _, seen := c[pkg+" "+name.Name]; ok && !seen {
								c[pkg+" "+name.Name] = v
								c[f.Name.Name+"."+name.Name] = v
								changed = true
							}
						}
					}
				}
			}
		}
	}
	return c
}

// fold is the value of e if it is constant, else the constant start of the
// string it builds, and whether any of e is constant.
func (c constants) fold(pkg string, e ast.Expr) (s string, ok, whole bool) {
	var parts []ast.Expr
	var flatten func(ast.Expr)
	flatten = func(e ast.Expr) {
		switch x := e.(type) {
		case *ast.BinaryExpr:
			if x.Op == token.ADD {
				flatten(x.X)
				flatten(x.Y)
				return
			}
		case *ast.ParenExpr:
			flatten(x.X)
			return
		}
		parts = append(parts, e)
	}
	flatten(e)
	var b strings.Builder
	for i, p := range parts {
		v, known := c.value(pkg, p)
		if !known {
			return b.String(), i > 0, false
		}
		b.WriteString(v)
	}
	return b.String(), true, true
}

func (c constants) value(pkg string, e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.Ident:
		v, ok := c[pkg+" "+x.Name]
		return v, ok
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			v, ok := c[id.Name+"."+x.Sel.Name]
			return v, ok
		}
	}
	return "", false
}

func calleeName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// readerNames are headerReaders and every function or function literal
// bound to a name that hands its own argument to a reader, as a call's
// argument or as an index.
func (src pinSource) readerNames() map[string]bool {
	readers := map[string]bool{}
	for _, r := range headerReaders {
		readers[r] = true
	}
	type fn struct {
		name   string
		params map[string]bool
		body   *ast.BlockStmt
	}
	var fns []fn
	paramsOf := func(t *ast.FuncType) map[string]bool {
		ps := map[string]bool{}
		for _, field := range t.Params.List {
			for _, n := range field.Names {
				ps[n.Name] = true
			}
		}
		return ps
	}
	for _, files := range src.files {
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.FuncDecl:
					if x.Body != nil {
						fns = append(fns, fn{x.Name.Name, paramsOf(x.Type), x.Body})
					}
				case *ast.AssignStmt:
					for i, rhs := range x.Rhs {
						if lit, ok := rhs.(*ast.FuncLit); ok && i < len(x.Lhs) {
							if id, ok := x.Lhs[i].(*ast.Ident); ok {
								fns = append(fns, fn{id.Name, paramsOf(lit.Type), lit.Body})
							}
						}
					}
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for _, f := range fns {
			if readers[f.name] {
				continue
			}
			passes := false
			ast.Inspect(f.body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if readers[calleeName(x)] {
						passes = passes || slices.ContainsFunc(x.Args, func(a ast.Expr) bool {
							id, ok := a.(*ast.Ident)
							return ok && f.params[id.Name]
						})
					}
				case *ast.IndexExpr:
					id, ok := x.Index.(*ast.Ident)
					passes = passes || ok && f.params[id.Name]
				}
				return !passes
			})
			if passes {
				readers[f.name], changed = true, true
			}
		}
	}
	return readers
}

// names is every x-amz- header name the files spell, with its use.
func (src pinSource) names() []amzName {
	consts := src.constants()
	readers := src.readerNames()
	var out []amzName
	for pkg, files := range src.files {
		for _, f := range files {
			seen := map[ast.Expr]bool{}
			var stack []ast.Node
			add := func(e ast.Expr, value string, prefix bool, use nameUse) {
				if name, ok := amzHeaderName(value); ok {
					out = append(out, amzName{
						key: exemptKey{pkg: pkg, decl: declName(stack), name: name, prefix: prefix, use: use},
						pos: src.fset.Position(e.Pos()),
					})
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				switch x := n.(type) {
				case *ast.BasicLit:
					top, at := topOfConcat(stack, x)
					if x.Kind == token.STRING && !seen[top] {
						seen[top] = true
						if v, ok, whole := consts.fold(pkg, top); ok || whole {
							add(top, v, !whole, useOf(stack[:at], top, readers))
						}
					}
				case *ast.CallExpr:
					// A reader called with a constant's name reads that name.
					if readers[calleeName(x)] {
						for _, a := range x.Args {
							switch a.(type) {
							case *ast.Ident, *ast.SelectorExpr:
								if v, ok := consts.value(pkg, a); ok {
									add(a, v, false, useRead)
								}
							}
						}
					}
				}
				stack = append(stack, n)
				return true
			})
		}
	}
	return out
}

// topOfConcat is the outermost concatenation lit is an operand of, or lit,
// and the depth of the stack above it.
func topOfConcat(stack []ast.Node, lit *ast.BasicLit) (ast.Expr, int) {
	var top ast.Expr = lit
	at := len(stack)
	for at > 0 {
		switch p := stack[at-1].(type) {
		case *ast.BinaryExpr:
			if p.Op != token.ADD {
				return top, at
			}
			top = p
		case *ast.ParenExpr:
			top = p
		default:
			return top, at
		}
		at--
	}
	return top, at
}

// useOf is how e, a child of stack's last node, is used.
func useOf(stack []ast.Node, e ast.Expr, readers map[string]bool) nameUse {
	if len(stack) == 0 {
		return useOther
	}
	switch p := stack[len(stack)-1].(type) {
	case *ast.CallExpr:
		if !slices.Contains(p.Args, e) {
			return useOther
		}
		switch name := calleeName(p); {
		case slices.Contains(headerWriters, name):
			return useWrite
		case readers[name]:
			return useRead
		}
	case *ast.IndexExpr:
		if p.Index != e {
			return useOther
		}
		if len(stack) > 1 {
			if as, ok := stack[len(stack)-2].(*ast.AssignStmt); ok && slices.Contains(as.Lhs, ast.Expr(p)) {
				return useWrite
			}
		}
		return useRead
	}
	return useOther
}

// declName names the top-level declaration the stack is in: a function, a
// method as its receiver's type and name, or a constant's or variable's
// first name.
func declName(stack []ast.Node) string {
	if len(stack) < 2 {
		return ""
	}
	switch d := stack[1].(type) {
	case *ast.FuncDecl:
		if d.Recv == nil || len(d.Recv.List) == 0 {
			return d.Name.Name
		}
		t := d.Recv.List[0].Type
		if star, ok := t.(*ast.StarExpr); ok {
			t = star.X
		}
		if id, ok := t.(*ast.Ident); ok {
			return id.Name + "." + d.Name.Name
		}
		return d.Name.Name
	case *ast.GenDecl:
		if len(stack) > 2 {
			switch s := stack[2].(type) {
			case *ast.ValueSpec:
				return s.Names[0].Name
			case *ast.TypeSpec:
				return s.Name.Name
			}
		}
	}
	return ""
}

// amzHeaderName is s as a lower-case x-amz- header name, from the name in
// any case or from its environment name, whose '_' and '-' are swapped.
func amzHeaderName(s string) (string, bool) {
	if lower := asciiLower(s); strings.HasPrefix(lower, "x-amz-") {
		return lower, true
	}
	if rest, ok := strings.CutPrefix(s, "HTTP_X_AMZ_"); ok {
		swapped := strings.Map(func(c rune) rune {
			switch c {
			case '_':
				return '-'
			case '-':
				return '_'
			}
			return c
		}, rest)
		return "x-amz-" + asciiLower(swapped), true
	}
	return "", false
}

// unrefused are the names a request read could take from X-Amz- alone that
// no entry of rawAmzDirectives begins and exempt does not list: every use
// but a write.
func unrefused(names []amzName, exempt map[exemptKey]string) []string {
	var out []string
	for _, n := range names {
		if _, ok := exempt[n.key]; ok || n.key.use == useWrite || isRawDirective(n.key.name) {
			continue
		}
		out = append(out, n.String())
	}
	slices.Sort(out)
	return out
}

// notRawReads are the uses of names no entry of rawAmzDirectives begins
// that take no directive from a request's X-Amz- header alone, each with
// why. An entry names the package, the declaration, the name and its use,
// so the same name read in another declaration, or read where it was only
// compared, is still found. A header rgw-go sends needs no entry: its name
// is a write's argument.
var notRawReads = map[exemptKey]string{
	{pkg: "internal/s3", decl: "metaPrefixes", name: "x-amz-"}:                             "init_meta_info's own prefix, read under every prefix",
	{pkg: "internal/s3", decl: "metaBlocklist", name: "x-amz-content-sha256"}:              "an x_meta_map name, not stored",
	{pkg: "internal/s3", decl: "metaBlocklist", name: "x-amz-checksum-algorithm"}:          "an x_meta_map name, not stored",
	{pkg: "internal/s3", decl: "metaBlocklist", name: "x-amz-date"}:                        "an x_meta_map name, not stored",
	{pkg: "internal/s3", decl: "cryptPrefix", name: "x-amz-server-side-en"}:                "crypt_attribute_map's prefix, read under every prefix",
	{pkg: "internal/s3", decl: "requestAttrs", name: "x-amz-meta-"}:                        "map_qs_metadata's query parameters",
	{pkg: "internal/s3", decl: "parseArgs", name: "x-amz-"}:                                "the query names RGWHTTPArgs lower-cases",
	{pkg: "internal/s3", decl: "credentialParams", name: "x-amz-signature"}:                "a presigned query parameter",
	{pkg: "internal/s3", decl: "credentialParams", name: "x-amz-credential"}:               "a presigned query parameter",
	{pkg: "internal/s3", decl: "attrHeaders", name: "x-amz-website-redirect-location"}:     "the response header an attr is sent as",
	{pkg: "internal/s3", decl: "endHeaderNames", name: "x-amz-request-id"}:                 "a response header the end of a GET keeps",
	{pkg: "internal/s3", decl: "endHeaderNames", name: "x-amz-request-charged"}:            "a response header the end of a GET keeps",
	{pkg: "internal/authz", decl: "metaPrefixes", name: "x-amz-"}:                          "init_meta_info's own prefix, read under every prefix",
	{pkg: "internal/authz", decl: "cryptAttr", name: "x-amz-", prefix: true}:               "a query parameter's name, compared",
	{pkg: "internal/admin", decl: "ParseArgs", name: "x-amz-"}:                             "the query names RGWHTTPArgs lower-cases",
	{pkg: "internal/auth", decl: "amzHeaderPrefixes", name: "x-amz-"}:                      "init_meta_info's own prefix",
	{pkg: "internal/auth", decl: "requestView.parseQuery", name: "x-amz-"}:                 "the query names RGWHTTPArgs lower-cases",
	{pkg: "internal/auth", decl: "amzHeadersV2", name: "x-amz-", prefix: true}:             "the name the string to sign files a renamed header under",
	{pkg: "internal/auth", decl: "relabelledDirective", name: "x-amz-", prefix: true}:      "the name relabelledDirective tests",
	{pkg: "internal/auth", decl: "relabelledDirective", name: "x-amz-"}:                    "the prefix relabelledDirective skips",
	{pkg: "internal/auth", decl: "qsMetaV2", name: "x-amz-meta-"}:                          "the query meta the presigned string to sign carries",
	{pkg: "internal/auth", decl: "canonicalStringV2", name: "x-amz-date", use: useRead}:    "SigV2's date, which empties the date line, so a move changes the string to sign or loses the date",
	{pkg: "internal/auth", decl: "discoverFlavour", name: "x-amz-algorithm", use: useRead}: "a presigned SigV4 query parameter",
	// SigV4's own inputs: its canonical headers name each header as sent, so
	// no prefix can be moved under its signature, and SigV2 takes no payload
	// form (payloadNone).
	{pkg: "internal/auth", decl: "payloadHashHeader", name: "x-amz-content-sha256"}:                        "SigV4's payload hash",
	{pkg: "internal/auth", decl: "expectedPayloadHash", name: "x-amz-content-sha256", use: useRead}:        "SigV4's payload hash",
	{pkg: "internal/auth", decl: "decodedContentLengthHeader", name: "x-amz-decoded-content-length"}:       "SigV4's aws-chunked length",
	{pkg: "internal/auth", decl: "authData.completer", name: "x-amz-decoded-content-length", use: useRead}: "SigV4's aws-chunked length",
	{pkg: "internal/auth", decl: "authData.completer", name: "x-amz-trailer", use: useRead}:                "SigV4's aws-chunked trailer names",
	{pkg: "internal/auth", decl: "trailerSignatureHeader", name: "x-amz-trailer-signature"}:                "a trailer field of SigV4's aws-chunked body",
	{pkg: "internal/auth", decl: "parseV4Header", name: "x-amz-date", use: useRead}:                        "SigV4's date, which it signs by name",
	{pkg: "internal/auth", decl: "parseV4Query", name: "x-amz-date", use: useRead}:                         "a presigned SigV4 query parameter",
	{pkg: "internal/auth", decl: "parseV4Query", name: "x-amz-credential", use: useRead}:                   "a presigned SigV4 query parameter",
	{pkg: "internal/auth", decl: "parseV4Query", name: "x-amz-expires", use: useRead}:                      "a presigned SigV4 query parameter",
	{pkg: "internal/auth", decl: "parseV4Query", name: "x-amz-signedheaders", use: useRead}:                "a presigned SigV4 query parameter",
	{pkg: "internal/auth", decl: "parseV4Query", name: "x-amz-signature", use: useRead}:                    "a presigned SigV4 query parameter",
	{pkg: "internal/auth", decl: "canonicalQueryV4", name: "x-amz-signature"}:                              "the query parameter SigV4's canonical query leaves out",
}

var _ = Describe("rawAmzDirectives", func() {
	var (
		src   pinSource
		names []amzName
	)

	BeforeEach(func() {
		src = loadPinSource(filepath.Join("..", ".."), "internal", "cmd")
		names = src.names()
	})

	It("walks every package under internal/ and cmd/, nested ones included", func() {
		Expect(src.files).To(SatisfyAll(
			HaveKey("internal/s3"), HaveKey("internal/authz"), HaveKey("internal/auth"),
			HaveKey("internal/cls/rgw"), HaveKey("cmd/rgw-go"),
		))
		Expect(src.files).NotTo(HaveKey("internal/op/opfakes"), "generated fakes")
	})

	It("refuses under another prefix every x-amz- name a request header is read by from X-Amz- alone", func() {
		Expect(unrefused(names, notRawReads)).To(BeEmpty(),
			"a name a package spells, but for a write's, begins with an entry of rawAmzDirectives, or notRawReads says why its use is no raw read")
	})

	It("holds only names the packages read or compare", func() {
		found := map[string]bool{}
		for _, n := range names {
			if n.key.use != useWrite {
				found[n.key.name] = true
			}
		}
		var unread []string
		for _, d := range rawAmzDirectives {
			if !found[d] {
				unread = append(unread, d)
			}
		}
		Expect(unread).To(BeEmpty(), "each entry is a name a package spells")
	})

	It("is not told a use it refuses, or one the packages no longer make, is no raw read", func() {
		found := map[exemptKey]bool{}
		for _, n := range names {
			found[n.key] = true
		}
		var stale []string
		for k := range notRawReads {
			if !found[k] || isRawDirective(k.name) {
				stale = append(stale, fmt.Sprintf("%+v", k))
			}
		}
		Expect(stale).To(BeEmpty(), "each notRawReads entry is a use the packages make that rawAmzDirectives does not refuse")
	})

	It("leaves the meta names to every prefix", func() {
		Expect(isRawDirective("x-amz-meta-")).To(BeFalse(), "no entry begins every x-amz-meta- name")
	})

	// Each source below is a mistake the pin must catch: a read the
	// exemptions would otherwise hide.
	Describe("finds a new raw read", func() {
		var module string

		BeforeEach(func() {
			module = GinkgoT().TempDir()
			for _, dir := range []string{"internal", "cmd"} {
				Expect(os.MkdirAll(filepath.Join(module, dir), 0o755)).To(Succeed())
			}
		})

		write := func(path, src string) {
			full := filepath.Join(module, path)
			Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(Succeed())
			Expect(os.WriteFile(full, []byte(src), 0o600)).To(Succeed())
		}
		found := func() []string { return unrefused(loadPinSource(module, "internal", "cmd").names(), notRawReads) }

		It("built by concatenation, constant or not, even in a declaration that compares a concatenated name", func() {
			write("internal/authz/env.go", `package authz
func cryptAttr(r *op.Request, name string) {
	_ = lowerASCII(k) == "x-amz-"+name
	op.HeaderValue(r.Header, "X-Amz-"+name)
}
func extra(r *op.Request) { op.HeaderValue(r.Header, "X-Amz-"+"Mfa") }
`)
			Expect(found()).To(ConsistOf(
				ContainSubstring("x-amz- (cryptAttr, read, prefix true)"),
				ContainSubstring("x-amz-mfa (extra, read, prefix false)"),
			))
		})

		It("of a name the same file sends as a response header", func() {
			write("internal/s3/getobject.go", `package s3
func header(r *op.Request, name string) string { v, _ := op.HeaderValue(r.Header, name); return v }
func setHeaders(h http.Header, v string) { addHeader(h, "x-amz-version-id", v); SetHeader(h, "x-amz-version-id", v) }
func getObjectInput(r *op.Request) string { return header(r, "x-amz-version-id") }
`)
			Expect(found()).To(ConsistOf(ContainSubstring("x-amz-version-id (getObjectInput, read, prefix false)")))
		})

		It("in a nested package or under cmd/", func() {
			write("internal/s3/sub/sub.go", `package sub
func f(h http.Header) string { return h.Get("X-Amz-Mfa") }
`)
			write("cmd/tool/main.go", `package main
const mfa = "x-amz-mfa"
func g(h http.Header) []string { return h.Values(mfa) }
`)
			Expect(found()).To(ConsistOf(
				SatisfyAll(ContainSubstring("internal/s3/sub/sub.go"), ContainSubstring("x-amz-mfa (f, read")),
				SatisfyAll(ContainSubstring("cmd/tool/main.go"), ContainSubstring("x-amz-mfa (mfa, other")),
				SatisfyAll(ContainSubstring("cmd/tool/main.go"), ContainSubstring("x-amz-mfa (g, read")),
			))
		})
	})
})
