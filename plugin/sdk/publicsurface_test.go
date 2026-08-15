package sdk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// The public surface of plugin/sdk (and its public sub-packages) must never NAME a type from an
// internal/ package — that is the single property letting an out-of-tree author import it. The Go
// compiler does NOT enforce it: an exported func may return an internal type, and only an external
// caller that tries to NAME that type fails to compile — a `v := Serve()` consumer never would. So
// a leak can hide until someone hits it. This check enforces it mechanically instead.
//
// It walks every non-test .go file under this package's directory, and for each EXPORTED
// declaration (types, funcs/methods on exported types, vars, consts) it flags any type expression
// that references an import whose path contains "/internal/". Break the property — e.g. add
// `func Leak() pluginpb.ScoreResponse` — and this test fails, naming the symbol and the type.
func TestPublicSurfaceNamesNoInternalType(t *testing.T) {
	fset := token.NewFileSet()
	var violations []string

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir // example plugins, not part of the public surface
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil // tests are not public surface and may name internal types freely
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		imports := importMap(f)
		for _, s := range exportedSignatures(f) {
			ast.Inspect(s.typ, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if p, ok := imports[id.Name]; ok && strings.Contains(p, "/internal/") {
					violations = append(violations,
						path+": exported "+s.name+" names "+id.Name+"."+sel.Sel.Name+" from "+p)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error("public surface leaks an internal type: " + v)
	}
}

// importMap maps each import's local qualifier to its path (handling aliases; skipping _ and .).
func importMap(f *ast.File) map[string]string {
	m := map[string]string{}
	for _, imp := range f.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		name := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		m[name] = p
	}
	return m
}

type signature struct {
	name string
	typ  ast.Node
}

// exportedSignatures returns the type expressions of every declaration that forms part of the
// package's public surface: exported types, exported funcs and methods on exported types, and
// exported vars/consts (their declared type).
func exportedSignatures(f *ast.File) []signature {
	var out []signature
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			if d.Recv != nil && !recvExported(d.Recv) {
				continue // a method on an unexported type is not public surface
			}
			if d.Recv != nil {
				out = append(out, signature{d.Name.Name, d.Recv})
			}
			out = append(out, signature{d.Name.Name, d.Type}) // FuncType: params + results
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						out = append(out, signature{s.Name.Name, s.Type})
					}
				case *ast.ValueSpec:
					if s.Type == nil {
						continue
					}
					for _, n := range s.Names {
						if n.IsExported() {
							out = append(out, signature{n.Name, s.Type})
							break
						}
					}
				}
			}
		}
	}
	return out
}

// recvExported reports whether a method receiver's base type is exported (handles *T and generic
// receivers T[…]).
func recvExported(recv *ast.FieldList) bool {
	if len(recv.List) == 0 {
		return false
	}
	t := recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch x := t.(type) {
	case *ast.Ident:
		return x.IsExported()
	case *ast.IndexExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.IsExported()
		}
	case *ast.IndexListExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.IsExported()
		}
	}
	return false
}
