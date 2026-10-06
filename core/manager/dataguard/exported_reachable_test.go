package dataguard

import (
	"go/ast"
	"go/parser"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这个包里的每个导出符号都必须有一个**生产调用方**。
//
// 这条守卫是冲着一份具体的实现写的：`RaisedClass`（决策 368 删掉的那个）
// 写着「It is the one function callers should use」，然后一个生产调用方都没有，
// 只有它自己的四条测试在调。它从包内读像"已实现"，从包外读像不存在——
// 而一个只有自己测试证明正确的东西，是这个仓库反复记录的失效形态。
//
// 守卫住在它要看的东西旁边，而不是放在 scripts/ 下。理由与决策 327 写在
// audit-port-check 里的一样：**有人改那个东西的时候，会不会同时看见闸门。**
//
// 它只在**本包**范围内成立。本包之外当然也有只被测试引用的导出符号——
// 那是别的包的账，由 deadcode 工具报、ratchet 闸门挡增长。
func TestEveryExportedSymbolHereHasAProductionCaller(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := repoRootOf(t, dir)

	decls := exportedDecls(t, dir)
	if len(decls) == 0 {
		t.Fatal("no exported symbol was read out of this package, so the check is " +
			"inspecting nothing; the parser has probably stopped matching")
	}
	for name, file := range decls {
		if !referencedOutsideTests(t, repoRoot, name) {
			t.Errorf("%s declares %s, and nothing outside a _test.go file mentions it.\n"+
				"  A symbol only its own tests reach is not a feature: it reads as "+
				"\"implemented\" from inside the package and as absent from outside it, and "+
				"its doc comment gets to say whatever it likes about who should call it.\n"+
				"  Either wire it into production or delete it — and if it was meant to be the "+
				"one callers use, say so in the same commit that makes it true.",
				filepath.Base(file), name)
		}
	}
}

// exportedDecls reads every top-level exported declaration in the package's
// non-test files. Methods are excluded on purpose: a method is reached through
// its receiver, and a receiver type with a live caller drags its methods along
// whether or not each one is spelled out anywhere.
func exportedDecls(t *testing.T, dir string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	out := map[string]string{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				switch decl := d.(type) {
				case *ast.FuncDecl:
					if decl.Recv != nil {
						continue
					}
					if decl.Name.IsExported() {
						out[decl.Name.Name] = f.Name.Name
					}
				case *ast.GenDecl:
					for _, spec := range decl.Specs {
						switch sp := spec.(type) {
						case *ast.TypeSpec:
							if sp.Name.IsExported() {
								out[sp.Name.Name] = f.Name.Name
							}
						case *ast.ValueSpec:
							for _, n := range sp.Names {
								if n.IsExported() {
									out[n.Name] = f.Name.Name
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}

// referencedOutsideTests parses every non-test Go file in the repository and
// collects the identifiers it actually uses.
//
// The first version of this was a textual search, and it **survived its own
// mutation**: the doc comment left behind when RaisedClass was deleted still
// spelled its name, so putting the function back did not make the guard go red.
// A doc comment that explains why something was removed is exactly the kind of
// sentence a text search cannot tell from code — and a guard that a comment can
// switch off is not a guard. Parsing is slower and buys the only property that
// matters here: a name counts only where the compiler would see it.
func referencedOutsideTests(t *testing.T, repoRoot, name string) bool {
	t.Helper()
	fset := token.NewFileSet()
	found := false
	walkErr := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, pErr := parser.ParseFile(fset, path, nil, 0)
		if pErr != nil {
			// A file this parser cannot read is a file this guard cannot
			// judge. Saying so is better than quietly not looking.
			return fmt.Errorf("parse %s: %w", path, pErr)
		}
		// The declaration itself is not a reference. Without this the guard
		// passes for every symbol it is supposed to police: declaring a
		// function puts its name in an ast.Ident, and a name that appears in
		// its own declaration has, by this rule, a production caller — which
		// is precisely the thing being checked. **A check that cannot fail on
		// the thing it checks is an assertion wearing a guard's clothes.**
		declared := declaredNamePositions(file)
		ast.Inspect(file, func(n ast.Node) bool {
			if found {
				return false
			}
			ident, ok := n.(*ast.Ident)
			if !ok || ident.Name != name {
				return true
			}
			if _, isDecl := declared[ident.Pos()]; isDecl {
				return true
			}
			found = true
			return false
		})
		return nil
	})
	if walkErr != nil {
		// A file this guard cannot read is a file it cannot judge. Swallowing
		// it would make an unreadable tree look like a clean one.
		t.Fatalf("walking %s for %q: %v", repoRoot, name, walkErr)
	}
	return found
}

// repoRootOf walks up to the directory holding go.work, falling back to the
// module's own go.mod. A test that cannot find the repository root is looking
// at a tree it cannot judge, so it says so rather than passing quietly.
func repoRootOf(t *testing.T, dir string) string {
	t.Helper()
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, "go.work")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatal("could not find the repository root from " + dir)
		}
		d = parent
	}
}

// declaredNamePositions is the set of identifier positions that belong to a
// declaration's own name, so a caller can tell "somebody used this" from
// "somebody wrote this down".
//
// Without it the guard passes for every symbol it is supposed to police:
// declaring a function puts its name in an ast.Ident, so by this rule a symbol
// that exists and nothing else would count as having a production caller.
// **A check that cannot fail on the thing it checks is an assertion wearing a
// guard's clothes.**
func declaredNamePositions(file *ast.File) map[token.Pos]bool {
	out := map[token.Pos]bool{}
	add := func(n *ast.Ident) {
		if n != nil {
			out[n.Pos()] = true
		}
	}
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			add(decl.Name)
			if decl.Recv != nil {
				for _, p := range decl.Recv.List {
					for _, n := range p.Names {
						add(n)
					}
				}
			}
			if decl.Type.Params != nil {
				for _, p := range decl.Type.Params.List {
					for _, n := range p.Names {
						add(n)
					}
				}
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					add(sp.Name)
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						add(n)
					}
				}
			}
		}
	}
	return out
}
