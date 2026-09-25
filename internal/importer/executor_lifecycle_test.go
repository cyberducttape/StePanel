package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecuteImport_RefusesExistingExtractRoot is the regression test for the
// self-collision bug in the old LifecycleAwareImporter: the provisioner
// created the site directory, then ExecuteImport rejected it as "already
// exists". Under the staging contract the extract target must be a fresh
// path; if the caller passes something that already exists, the executor
// refuses before touching anything.
func TestExecuteImport_RefusesExistingExtractRoot(t *testing.T) {
	tmp := t.TempDir()
	extractRoot := filepath.Join(tmp, "staging-collision")
	if err := os.MkdirAll(extractRoot, 0750); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(extractRoot, "keep")
	if err := os.WriteFile(sentinel, []byte("preexisting"), 0600); err != nil {
		t.Fatal(err)
	}

	executor := NewExecutor()
	_, err := executor.ExecuteImport(
		context.Background(),
		&ArchiveImportRequest{
			SiteName:   "site1",
			URL:        "https://example.invalid/x.tar.gz",
			ConfigPath: "wp-config.php",
		},
		extractRoot,
		func(*ImportJob) {},
	)
	if err == nil {
		t.Fatal("ExecuteImport should refuse an existing extract root")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}

	// The pre-existing file must be untouched — the executor must not have
	// walked into the directory it refused.
	data, readErr := os.ReadFile(sentinel)
	if readErr != nil {
		t.Fatalf("sentinel file was disturbed: %v", readErr)
	}
	if string(data) != "preexisting" {
		t.Errorf("sentinel content was rewritten: got %q", data)
	}
}

// TestValidateSiteCreation_AcceptsFreshExtractRoot verifies the flipside:
// a nonexistent path with a writable parent passes validation, so the
// orchestrator's staging path is a valid target.
func TestValidateSiteCreation_AcceptsFreshExtractRoot(t *testing.T) {
	tmp := t.TempDir()
	extractRoot := filepath.Join(tmp, "staging-fresh")

	executor := &Executor{}
	job := &ImportJob{SiteName: "site1", WebRoot: extractRoot}

	if err := executor.validateSiteCreation(job); err != nil {
		t.Fatalf("validateSiteCreation on fresh path failed: %v", err)
	}
	// validateSiteCreation is allowed to create the parent, but must not
	// pre-create the extract root itself — that happens during extraction.
	if _, err := os.Stat(extractRoot); !os.IsNotExist(err) {
		t.Errorf("validateSiteCreation should leave extract root uncreated, stat err: %v", err)
	}
}

// TestValidateSiteCreation_RejectsExistingPath is the unit-level regression
// test complementing the ExecuteImport-level one above.
func TestValidateSiteCreation_RejectsExistingPath(t *testing.T) {
	tmp := t.TempDir()
	executor := &Executor{}
	job := &ImportJob{SiteName: "site1", WebRoot: tmp}
	err := executor.validateSiteCreation(job)
	if err == nil {
		t.Fatal("validateSiteCreation should refuse an existing path")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}
