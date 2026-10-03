package main

import (
	"os"
	"strings"
	"testing"
)

// platformOnlyScope names 2.0 shared-hosting platform work. The roadmap
// defines 1.0 as a single-host, operator-managed product, so none of these
// may appear as a 1.0 requirement.
var platformOnlyScope = []string{
	"high-availability datastore",
	"multi-region replication",
	"sla monitoring",
	"automated incident response",
	"enterprise support tiers",
	"cross-host durable job routing",
}

// TestOneDotZeroScopeExcludesPlatformWork keeps the 1.0 definition fixed
// across the readiness, gate, and roadmap documents. Without it, platform
// infrastructure can drift into the 1.0 path and 1.0 never has a finish line.
func TestOneDotZeroScopeExcludesPlatformWork(t *testing.T) {
	for _, tc := range []struct{ path, heading string }{
		{"docs/PRODUCTION_READINESS.md", "## Path to 1.0"},
		{"docs/V1_PRODUCTION_GATES.md", "## v1.0.0 Release Checklist"},
		{"docs/ROADMAP.md", "## 1.0 "},
	} {
		section := markdownSection(t, tc.path, tc.heading)
		for _, line := range strings.Split(section, "\n") {
			// The scope statements name excluded work explicitly; only
			// checklist and list items are requirements.
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "- ") {
				continue
			}
			lower := strings.ToLower(trimmed)
			for _, item := range platformOnlyScope {
				if strings.Contains(lower, item) {
					t.Errorf("%s %q lists 2.0 platform work as a 1.0 requirement: %s", tc.path, tc.heading, trimmed)
				}
			}
		}
	}

	readiness := markdownSection(t, "docs/PRODUCTION_READINESS.md", "## After 1.0")
	for _, item := range platformOnlyScope {
		if !strings.Contains(strings.ToLower(readiness), item) {
			t.Errorf("docs/PRODUCTION_READINESS.md must track %q under the post-1.0 platform section", item)
		}
	}
}

// markdownSection returns the text from the first line starting with heading
// up to the next heading of the same or higher level.
func markdownSection(t *testing.T, path, heading string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	level := strings.IndexFunc(heading, func(r rune) bool { return r != '#' })
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, heading) {
			continue
		}
		var section []string
		for _, next := range lines[i+1:] {
			if hashes := strings.IndexFunc(next, func(r rune) bool { return r != '#' }); hashes > 0 && hashes <= level && strings.HasPrefix(next[hashes:], " ") {
				break
			}
			section = append(section, next)
		}
		return strings.Join(section, "\n")
	}
	t.Fatalf("%s: heading %q not found", path, heading)
	return ""
}
