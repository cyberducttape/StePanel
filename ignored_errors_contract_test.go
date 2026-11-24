package stepanel

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// durableOSFunctions create or change persistent filesystem state; CLAUDE.md
// requires their errors to be checked. os.Remove and os.RemoveAll are left
// out: in this codebase they overwhelmingly delete temporary files on an
// already-failing path, while canonical deletions go through SiteManager and
// the root broker with checked errors.
var durableOSFunctions = map[string]bool{
	"Mkdir": true, "MkdirAll": true, "WriteFile": true, "Rename": true,
	"Chmod": true, "Chown": true, "Lchown": true, "Link": true, "Symlink": true,
	"Truncate": true,
}

// durableMethods persist or commit state when called on files, roots,
// transactions, or databases.
var durableMethods = map[string]bool{
	"Sync": true, "Commit": true, "Truncate": true, "Chmod": true, "Chown": true,
	"Exec": true, "ExecContext": true, "WriteFile": true, "Rename": true,
	"MkdirAll": true, "Mkdir": true,
}

const bestEffortMarker = "best-effort:"

// TestDurableMutationErrorsAreChecked fails when production code discards the
// error from a durable filesystem or database mutation, either as a bare call
// statement or by assigning every result to "_". Intentional cleanup on an
// already-failing path must say why with a "// best-effort: <reason>" comment
// on the same or the preceding line, so every exception is reviewable.
func TestDurableMutationErrorsAreChecked(t *testing.T) {
	var violations []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", "testdata", ".git", "sdk", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		justified := map[int]bool{}
		for _, group := range file.Comments {
			for _, comment := range group.List {
				if text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//")); strings.HasPrefix(text, bestEffortMarker) && len(strings.TrimSpace(strings.TrimPrefix(text, bestEffortMarker))) > 0 {
					justified[fileSet.Position(comment.Slash).Line] = true
				}
			}
		}
		report := func(call *ast.CallExpr) {
			name, durable := durableCallName(call)
			if !durable {
				return
			}
			line := fileSet.Position(call.Pos()).Line
			if justified[line] || justified[line-1] {
				return
			}
			violations = append(violations, fileSet.Position(call.Pos()).String()+": "+name)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch statement := node.(type) {
			case *ast.ExprStmt:
				if call, ok := statement.X.(*ast.CallExpr); ok {
					report(call)
				}
			case *ast.AssignStmt:
				if len(statement.Rhs) != 1 || !allBlank(statement.Lhs) {
					return true
				}
				if call, ok := statement.Rhs[0].(*ast.CallExpr); ok {
					report(call)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("durable mutation errors are discarded; check them, or justify cleanup with %q:\n  %s", "// "+bestEffortMarker+" <reason>", strings.Join(violations, "\n  "))
	}
}

func durableCallName(call *ast.CallExpr) (string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "os" {
		return "os." + selector.Sel.Name, durableOSFunctions[selector.Sel.Name]
	}
	return "." + selector.Sel.Name, durableMethods[selector.Sel.Name]
}

func allBlank(expressions []ast.Expr) bool {
	for _, expression := range expressions {
		if ident, ok := expression.(*ast.Ident); !ok || ident.Name != "_" {
			return false
		}
	}
	return true
}
