package stepanel

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Auth.Require writes a fail-closed audit event before every unsafe request
// reaches its handler. A mutating route registered outside it must audit on
// its own; this list is the reviewed set that does.
var publicMutatingRoutes = map[string]string{
	"/login":                  "audits auth.login.succeeded (fail-closed), failed, and throttled",
	"/logout":                 "audits auth.logout",
	"/api/sites/git-webhook/": "audits webhook.deploy.accepted (fail-closed) before deploying",
}

func TestEveryMutatingRouteIsAudited(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "main.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Handle" {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || receiver.Name != "mux" {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		path, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Errorf("invalid route literal %s: %v", literal.Value, err)
			return true
		}
		var rendered strings.Builder
		if err := format.Node(&rendered, fileSet, call.Args[1]); err != nil {
			t.Errorf("render route %s: %v", path, err)
			return true
		}
		chain := rendered.String()
		if !strings.Contains(chain, "MethodPost") && !strings.Contains(chain, "MethodPut") && !strings.Contains(chain, "MethodPatch") && !strings.Contains(chain, "MethodDelete") {
			return true
		}
		checked++
		if strings.Contains(chain, "Auth.Require(") || strings.Contains(chain, "Auth.RequireAdministrator(") {
			return true
		}
		if _, reviewed := publicMutatingRoutes[path]; !reviewed {
			t.Errorf("mutating route %s is not behind Auth.Require and is not a reviewed self-auditing route", path)
		}
		return true
	})
	if checked < 50 {
		t.Fatalf("only %d mutating routes found; the route pattern no longer matches main.go", checked)
	}
}

// The v1 release checklist requires no deferred-work markers in critical
// paths: production Go code, the root broker, and the root helper scripts.
func TestNoDeferredWorkMarkersInCriticalPaths(t *testing.T) {
	// Stub phrasing catches placeholder logic that pretends to succeed,
	// which is worse than a TODO because it reads like finished code.
	marker := regexp.MustCompile(`\b(TODO|FIXME|HACK)\b|(?i)\bin (a )?real implementation\b|\bfor now,? just (mark|verify|return)\b`)
	var paths []string
	for _, pattern := range []string{"*.go", "internal/*/*.go", "cmd/*/*.go", "deploy/integrations/*", "install.sh"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, matches...)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for number, line := range strings.Split(string(data), "\n") {
			if marker.MatchString(line) {
				t.Errorf("%s:%d: %s", path, number+1, strings.TrimSpace(line))
			}
		}
	}
}
