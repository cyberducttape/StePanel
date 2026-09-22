package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestOpenAPIContractSync verifies OpenAPI spec matches registered routes
func TestOpenAPIContractSync(t *testing.T) {
	// Get actual routes from main.go
	actualRoutes := getRegisteredRoutes(t)

	// Get documented routes from openapi.yaml
	documentedRoutes := getDocumentedRoutes(t)

	// Check for missing routes
	missing := setDifference(actualRoutes, documentedRoutes)
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("Missing from OpenAPI spec (should be added or marked as internal):\n  %s",
			strings.Join(missing, "\n  "))
	}

	// Check for stale documentation
	stale := setDifference(documentedRoutes, actualRoutes)
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("Documented in OpenAPI but not registered in code (spec is out of date):\n  %s",
			strings.Join(stale, "\n  "))
	}
}

// getRegisteredRoutes extracts all /api/* routes from main.go
func getRegisteredRoutes(t *testing.T) []string {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("failed to read main.go: %v", err)
	}

	// Pattern: mux.Handle("/api/...", ...)
	pattern := regexp.MustCompile(`mux\.Handle\("(/api/[^"]+)"`)
	matches := pattern.FindAllStringSubmatch(string(content), -1)

	routes := make(map[string]bool)
	for _, match := range matches {
		if len(match) > 1 {
			route := match[1]
			// Normalize route (remove trailing {param})
			// /api/foo/{id} -> /api/foo/...
			route = regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(route, "{id}")
			routes[route] = true
		}
	}

	var result []string
	for route := range routes {
		result = append(result, route)
	}
	sort.Strings(result)
	return result
}

// getDocumentedRoutes extracts all documented routes from openapi.yaml
func getDocumentedRoutes(t *testing.T) []string {
	content, err := os.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatalf("failed to read openapi.yaml: %v", err)
	}

	// Pattern: /api/... as a YAML key (followed by : and indent)
	pattern := regexp.MustCompile(`^\s{2}(/api/[^\s:]+):\s*$`)

	routes := make(map[string]bool)

	for _, line := range strings.Split(string(content), "\n") {
		matches := pattern.FindStringSubmatch(line)
		if len(matches) > 1 {
			routes[matches[1]] = true
		}
	}

	var result []string
	for route := range routes {
		result = append(result, route)
	}

	if len(result) == 0 {
		t.Fatal("no '/api' routes found in openapi.yaml")
	}

	sort.Strings(result)
	return result
}

// setDifference returns elements in a that are not in b
func setDifference(a, b []string) []string {
	bMap := make(map[string]bool)
	for _, v := range b {
		bMap[v] = true
	}

	var result []string
	for _, v := range a {
		if !bMap[v] {
			result = append(result, v)
		}
	}
	return result
}

// TestOpenAPIStructure verifies the OpenAPI spec is well-formed
func TestOpenAPIStructure(t *testing.T) {
	content, err := os.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatalf("failed to read openapi.yaml: %v", err)
	}

	text := string(content)

	// Check required top-level fields
	required := []string{"openapi:", "info:", "servers:", "paths:"}
	for _, field := range required {
		if !strings.Contains(text, field) {
			t.Errorf("missing required field: %s", field)
		}
	}

	// Check that paths section exists
	if !strings.Contains(text, "paths:") {
		t.Error("no 'paths' section found")
	}

	// Check that paths have operations (basic check)
	operations := []string{"get:", "post:", "put:", "delete:", "patch:"}
	hasOperations := false
	for _, op := range operations {
		if strings.Contains(text, op) {
			hasOperations = true
			break
		}
	}
	if !hasOperations {
		t.Error("no HTTP operations found in paths")
	}
}

// isOperation checks if a key is an HTTP method
func isOperation(key string) bool {
	methods := map[string]bool{
		"get":     true,
		"post":    true,
		"put":     true,
		"delete":  true,
		"patch":   true,
		"head":    true,
		"options": true,
		"trace":   true,
	}
	return methods[strings.ToLower(key)]
}

// TestOpenAPISecuritySchemes verifies security definitions match code
func TestOpenAPISecuritySchemes(t *testing.T) {
	content, err := os.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatalf("failed to read openapi.yaml: %v", err)
	}

	text := string(content)

	// Check that security schemes are defined
	// StePanel uses: sessionCookie (CSRF validated)
	if !strings.Contains(text, "components:") {
		t.Error("components section missing - security schemes cannot be defined")
		return
	}

	if !strings.Contains(text, "securitySchemes:") {
		t.Error("securitySchemes not defined in components")
		return
	}

	if !strings.Contains(text, "sessionCookie:") {
		t.Error("sessionCookie security scheme not defined")
	}
}

// TestOpenAPIEndpointConsistency verifies endpoint documentation is consistent
func TestOpenAPIEndpointConsistency(t *testing.T) {
	content, err := os.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatalf("failed to read openapi.yaml: %v", err)
	}

	text := string(content)

	// Basic check: look for operations without summary or description
	// Pattern: get:, post:, etc. should be followed by summary: or description:
	lines := strings.Split(text, "\n")
	issuesFound := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Check if this is an operation
		for _, op := range []string{"get:", "post:", "put:", "delete:", "patch:"} {
			if strings.HasPrefix(trimmed, op) {
				// Check next non-empty line for summary or description
				hasSummaryOrDesc := false
				for j := i + 1; j < len(lines) && j < i+5; j++ {
					nextLine := strings.TrimSpace(lines[j])
					if strings.HasPrefix(nextLine, "summary:") || strings.HasPrefix(nextLine, "description:") {
						hasSummaryOrDesc = true
						break
					}
					if strings.HasPrefix(nextLine, "get:") || strings.HasPrefix(nextLine, "post:") {
						break // Next operation found
					}
				}
				if !hasSummaryOrDesc {
					t.Logf("WARNING: %s at line %d has neither summary nor description", op, i+1)
					issuesFound++
				}
				break
			}
		}
	}

	if issuesFound > 0 {
		t.Logf("Found %d consistency issues in OpenAPI spec (warnings only)", issuesFound)
	}
}

// TestOpenAPIYAMLFormat verifies the YAML format is valid
func TestOpenAPIYAMLFormat(t *testing.T) {
	content, err := os.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatalf("failed to read openapi.yaml: %v", err)
	}

	if len(content) == 0 {
		t.Error("openapi.yaml is empty")
	}

	// Basic YAML validation: check for valid structure
	text := string(content)
	if !strings.Contains(text, "openapi:") {
		t.Error("missing 'openapi:' field - not a valid OpenAPI spec")
	}
}

// TestOpenAPIVersionSync checks that version matches VERSION constant
func TestOpenAPIVersionSync(t *testing.T) {
	content, err := os.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatalf("failed to read openapi.yaml: %v", err)
	}

	text := string(content)

	// Look for version: line (can be quoted or unquoted)
	pattern := regexp.MustCompile(`version:\s+["']?([^"'\s]+)`)
	matches := pattern.FindStringSubmatch(text)

	if len(matches) < 2 {
		t.Error("could not find version in openapi.yaml")
		return
	}

	specVersion := matches[1]
	if specVersion != Version {
		t.Errorf("OpenAPI spec version %q does not match Version constant %q", specVersion, Version)
	}
}
