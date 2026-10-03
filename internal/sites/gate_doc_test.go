package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestGateOneDocumentMatchesManager keeps the release gate honest: the Gate 1
// flow must not route an operation to a manager method that returns
// ErrNotImplemented, and the gate's "Not implemented" list must name exactly
// the methods that do.
func TestGateOneDocumentMatchesManager(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sites", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := NewDefaultManager(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	placeholders := map[string]error{
		"ImportArchive":       func() error { _, err := m.ImportArchive(ctx, &ImportRequest{Name: "demo"}); return err }(),
		"Restore":             func() error { _, err := m.Restore(ctx, &RestoreRequest{Name: "demo"}); return err }(),
		"UpdateConfiguration": m.UpdateConfiguration(ctx, "demo", &UpdateRequest{}),
		"Suspend":             m.Suspend(ctx, "demo", "test"),
		"Resume":              m.Resume(ctx, "demo"),
	}
	var unimplemented []string
	for name, err := range placeholders {
		if errors.Is(err, ErrNotImplemented) {
			unimplemented = append(unimplemented, name)
		}
	}
	sort.Strings(unimplemented)

	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "V1_PRODUCTION_GATES.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	start := strings.Index(doc, "## Gate 1:")
	end := strings.Index(doc, "## Gate 2:")
	if start < 0 || end < start {
		t.Fatal("Gate 1 section not found")
	}
	gate := doc[start:end]

	flowStart := strings.Index(gate, "```\n")
	flowEnd := strings.Index(gate, "# Never acceptable:")
	if flowStart < 0 || flowEnd < flowStart {
		t.Fatal("Gate 1 flow block not found")
	}
	flow := gate[flowStart:flowEnd]
	for _, name := range unimplemented {
		if strings.Contains(flow, "SiteManager."+name+"(") {
			t.Errorf("Gate 1 flow routes an operation to SiteManager.%s, which returns ErrNotImplemented", name)
		}
	}

	listed := regexp.MustCompile("\\*\\*Not implemented in `SiteManager`\\*\\*[^:]*:([^.]*)\\.").FindStringSubmatch(gate)
	if listed == nil {
		t.Fatal("Gate 1 has no \"Not implemented in SiteManager\" list")
	}
	var documented []string
	for _, match := range regexp.MustCompile("`([A-Za-z]+)`").FindAllStringSubmatch(listed[1], -1) {
		documented = append(documented, match[1])
	}
	sort.Strings(documented)
	if strings.Join(documented, ",") != strings.Join(unimplemented, ",") {
		t.Errorf("Gate 1 lists unimplemented methods %v, manager has %v", documented, unimplemented)
	}
}
