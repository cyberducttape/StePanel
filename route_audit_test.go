package main

import (
	"os"
	"path/filepath"
	"regexp"
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
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	route := regexp.MustCompile(`mux\.Handle\("([^"]+)", (.*)\)\s*$`)
	checked := 0
	for _, line := range strings.Split(string(source), "\n") {
		match := route.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		path, chain := match[1], match[2]
		if !strings.Contains(chain, "MethodPost") && !strings.Contains(chain, "MethodPut") && !strings.Contains(chain, "MethodPatch") && !strings.Contains(chain, "MethodDelete") {
			continue
		}
		checked++
		if strings.Contains(chain, "Auth.Require(") || strings.Contains(chain, "Auth.RequireAdministrator(") {
			continue
		}
		if _, reviewed := publicMutatingRoutes[path]; !reviewed {
			t.Errorf("mutating route %s is not behind Auth.Require and is not a reviewed self-auditing route", path)
		}
	}
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
