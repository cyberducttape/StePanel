package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newManager(t *testing.T) (*DefaultManager, string) {
	t.Helper()
	root := t.TempDir()
	m, err := NewDefaultManager(root)
	if err != nil {
		t.Fatalf("NewDefaultManager: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sites"), 0750); err != nil {
		t.Fatal(err)
	}
	return m, root
}

func TestNewDefaultManagerRejectsUnsafeRoot(t *testing.T) {
	for _, bad := range []string{"", ".", "relative/path", "./relative"} {
		if _, err := NewDefaultManager(bad); err == nil {
			t.Errorf("NewDefaultManager(%q) should have failed", bad)
		}
	}
	if _, err := NewDefaultManager("/tmp"); err != nil {
		t.Errorf("NewDefaultManager on absolute path failed: %v", err)
	}
}

// TestDeleteRefusesInvalidNames is the direct trust-boundary regression:
// the manager MUST NOT rm -rf a path derived from a malformed name, even
// if all HTTP callers claim they pre-validated. Every case below would
// have been caught by validSiteName; we still verify Delete rejects them
// because a regression in the HTTP validator must not turn into
// filesystem damage.
func TestDeleteRefusesInvalidNames(t *testing.T) {
	m, root := newManager(t)
	// Pre-populate a real site so a valid-name Delete has something to remove
	// and we can prove invalid-name Deletes leave it alone.
	sentinelDir := filepath.Join(root, "sites", "alpha")
	if err := os.MkdirAll(sentinelDir, 0750); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"",                      // empty
		"..",                    // parent traversal
		"../etc",                // relative escape
		"/etc",                  // absolute — not matched by validSiteName
		"a/b",                   // slash — not matched
		"UPPERCASE",             // uppercase — not matched
		"has spaces",            // whitespace — not matched
		strings.Repeat("x", 33), // too long
		"name\x00null",          // NUL
		"name;rm -rf /",         // shell metacharacters
	} {
		err := m.Delete(context.Background(), bad)
		if err == nil {
			t.Errorf("Delete(%q) should have refused", bad)
		}
		if _, statErr := os.Stat(sentinelDir); statErr != nil {
			t.Fatalf("Delete(%q) removed unrelated site: %v", bad, statErr)
		}
	}
	// The valid site is still removable.
	if err := m.Delete(context.Background(), "alpha"); err != nil {
		t.Errorf("Delete of valid site failed: %v", err)
	}
	if _, err := os.Stat(sentinelDir); !os.IsNotExist(err) {
		t.Errorf("Delete did not remove the site directory: %v", err)
	}
}

// TestDeleteRefusesSymlinkedSite is the second half of the trust boundary:
// if the site directory has been replaced with a symlink between
// provisioning and deletion, the manager MUST NOT follow it into another
// tree. helper.SafePath rejects symlink *parents*, and the leaf check in
// Delete rejects the site path itself being a symlink.
func TestDeleteRefusesSymlinkedSite(t *testing.T) {
	m, root := newManager(t)
	// Create a "target" tree the attacker would like us to rm -rf.
	target := t.TempDir()
	victim := filepath.Join(target, "important")
	if err := os.WriteFile(victim, []byte("do not delete"), 0600); err != nil {
		t.Fatal(err)
	}
	// Replace the site dir with a symlink to that other tree.
	sitePath := filepath.Join(root, "sites", "malicious")
	if err := os.Symlink(target, sitePath); err != nil {
		t.Fatal(err)
	}
	err := m.Delete(context.Background(), "malicious")
	if err == nil {
		t.Fatal("Delete followed a symlink; should have refused")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error should mention symlink refusal, got: %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("Delete followed symlink and touched target: %v", err)
	}
}

// TestDeleteAbsentSiteIsIdempotent — deleting a site that does not exist
// is a no-op, not an error. Matches how the reconciler-style callers use it.
func TestDeleteAbsentSiteIsIdempotent(t *testing.T) {
	m, _ := newManager(t)
	if err := m.Delete(context.Background(), "never-existed"); err != nil {
		t.Errorf("Delete of absent site should be nil, got: %v", err)
	}
}

// TestPlaceholdersReturnErrNotImplemented — Restore/Update/Suspend/Resume no
// longer silently succeed. A caller that treated the old nil return as "done"
// would have been actively lied to.
func TestPlaceholdersReturnErrNotImplemented(t *testing.T) {
	m, root := newManager(t)
	if err := os.MkdirAll(filepath.Join(root, "sites", "existing", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := m.Restore(ctx, &RestoreRequest{Name: "existing"}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Restore: want ErrNotImplemented, got %v", err)
	}
	if _, err := m.ImportArchive(ctx, &ImportRequest{Name: "new"}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("ImportArchive: want ErrNotImplemented, got %v", err)
	}
	if err := m.UpdateConfiguration(ctx, "existing", &UpdateRequest{}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("UpdateConfiguration: want ErrNotImplemented, got %v", err)
	}
	if err := m.Suspend(ctx, "existing", "test"); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Suspend: want ErrNotImplemented, got %v", err)
	}
	if err := m.Resume(ctx, "existing"); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Resume: want ErrNotImplemented, got %v", err)
	}
}

func TestCloneStagesAndAtomicallyPublishesSite(t *testing.T) {
	m, root := newManager(t)
	source := filepath.Join(root, "sites", "source", "public")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "index.php"), []byte("<?php echo 'ok';"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "data.txt"), []byte("nested"), 0600); err != nil {
		t.Fatal(err)
	}

	cloned, err := m.Clone(context.Background(), &CloneRequest{SourceName: "source", DestName: "copy", AccountOwner: "customer"})
	if err != nil {
		t.Fatalf("Clone failed: %v", err)
	}
	if cloned.WebRoot != filepath.Join(root, "sites", "copy", "public") {
		t.Fatalf("clone WebRoot = %q", cloned.WebRoot)
	}
	for _, name := range []string{"index.php", filepath.Join("nested", "data.txt")} {
		data, readErr := os.ReadFile(filepath.Join(cloned.WebRoot, name))
		if readErr != nil {
			t.Fatalf("cloned %s missing: %v", name, readErr)
		}
		if len(data) == 0 {
			t.Fatalf("cloned %s is empty", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "sites", ".stepanel-manager-staging")); err != nil {
		t.Fatalf("clone staging root missing: %v", err)
	}
	if _, err := m.Clone(context.Background(), &CloneRequest{SourceName: "source", DestName: "copy"}); err == nil {
		t.Fatal("second clone should reject an existing destination")
	}
}

func TestCloneRejectsSymlinkWithoutPublishingDestination(t *testing.T) {
	m, root := newManager(t)
	source := filepath.Join(root, "sites", "source", "public")
	if err := os.MkdirAll(source, 0750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Clone(context.Background(), &CloneRequest{SourceName: "source", DestName: "copy"}); err == nil {
		t.Fatal("Clone followed a symlink")
	}
	if _, err := os.Stat(filepath.Join(root, "sites", "copy")); !os.IsNotExist(err) {
		t.Fatalf("failed clone published destination: %v", err)
	}
}

func TestActivateStagedPublishesPublicTree(t *testing.T) {
	m, root := newManager(t)
	staged := filepath.Join(root, "sites", ".import-staging", "job-1")
	if err := os.MkdirAll(staged, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "index.html"), []byte("imported"), 0640); err != nil {
		t.Fatal(err)
	}

	site, err := m.ActivateStaged(context.Background(), "imported", staged)
	if err != nil {
		t.Fatalf("ActivateStaged failed: %v", err)
	}
	expected := filepath.Join(root, "sites", "imported", "public")
	if site.WebRoot != expected {
		t.Fatalf("activated WebRoot = %q, want %q", site.WebRoot, expected)
	}
	if data, err := os.ReadFile(filepath.Join(expected, "index.html")); err != nil || string(data) != "imported" {
		t.Fatalf("activated content = %q, err = %v", data, err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged tree still exists: %v", err)
	}
}

func TestActivateStagedRejectsUnsafeOrInvalidTrees(t *testing.T) {
	m, root := newManager(t)
	outside := t.TempDir()
	if _, err := m.ActivateStaged(context.Background(), "outside", outside); err == nil {
		t.Fatal("ActivateStaged accepted a staged path outside the web root")
	}
	ordinary := filepath.Join(root, "sites", "ordinary")
	if err := os.MkdirAll(ordinary, 0750); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ActivateStaged(context.Background(), "ordinary-target", ordinary); err == nil {
		t.Fatal("ActivateStaged accepted a non-manager-owned direct site path")
	}
	outsideStage := filepath.Join(root, "outside-stage")
	if err := os.MkdirAll(outsideStage, 0750); err != nil {
		t.Fatal(err)
	}
	symlinkStage := filepath.Join(root, "sites", ".import-staging", "job-link")
	if err := os.MkdirAll(filepath.Dir(symlinkStage), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideStage, symlinkStage); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ActivateStaged(context.Background(), "symlink-target", symlinkStage); err == nil {
		t.Fatal("ActivateStaged accepted a symlinked staging tree")
	}

	parent := filepath.Join(root, "sites", ".import-staging", "job-2")
	if err := os.MkdirAll(filepath.Join(parent, "public"), 0750); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ActivateStaged(context.Background(), "nested", parent); err != nil {
		t.Fatalf("ActivateStaged rejected a valid tree containing public/: %v", err)
	}

	canceled := filepath.Join(root, "sites", ".import-staging", "job-3")
	if err := os.MkdirAll(canceled, 0750); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.ActivateStaged(ctx, "canceled", canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled activation error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sites", "canceled")); !os.IsNotExist(err) {
		t.Fatalf("canceled activation published a destination: %v", err)
	}
}

func TestActivateStagedAcceptsManagerOwnedRollbackRelease(t *testing.T) {
	m, root := newManager(t)
	siteRoot := filepath.Join(root, "sites", "rollback", "public")
	previous := filepath.Join(root, "sites", "rollback", ".stepanel-previous-old")
	if err := os.MkdirAll(previous, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous, "index.html"), []byte("previous"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ActivateStaged(context.Background(), "rollback", previous); err != nil {
		t.Fatalf("ActivateStaged rejected manager-owned rollback release: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(siteRoot, "index.html"))
	if err != nil || string(data) != "previous" {
		t.Fatalf("rollback activation content = %q, error = %v", data, err)
	}
}

// TestPlaceholdersStillValidateName — a placeholder returning ErrNotImplemented
// must still refuse a malformed name up front, so a caller that catches
// ErrNotImplemented specifically doesn't mask input validation errors.
func TestPlaceholdersStillValidateName(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	if err := m.UpdateConfiguration(ctx, "../etc", &UpdateRequest{}); err == nil || errors.Is(err, ErrNotImplemented) {
		t.Errorf("UpdateConfiguration with bad name should fail on validation, got %v", err)
	}
	if err := m.Suspend(ctx, "UPPERCASE", "test"); err == nil || errors.Is(err, ErrNotImplemented) {
		t.Errorf("Suspend with bad name should fail on validation, got %v", err)
	}
}

// TestCreateProvisionsUnderWebRoot — end-to-end smoke test for the one
// method that does real work. Also confirms the WebRoot returned in the
// Site value is under the manager's configured root, not caller-supplied.
func TestCreateProvisionsUnderWebRoot(t *testing.T) {
	m, root := newManager(t)
	site, err := m.Create(context.Background(), &CreateRequest{Name: "fresh"})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	expected := filepath.Join(root, "sites", "fresh", "public")
	if site.WebRoot != expected {
		t.Errorf("Site.WebRoot = %q, want %q", site.WebRoot, expected)
	}
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("Create did not produce the public/ directory: %v", err)
	}
	// A second Create for the same name must fail (no accidental overwrite).
	if _, err := m.Create(context.Background(), &CreateRequest{Name: "fresh"}); err == nil {
		t.Error("second Create for the same name should have failed")
	}
}

func TestCreateHonorsCancellationBeforePublication(t *testing.T) {
	m, root := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Create(ctx, &CreateRequest{Name: "cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create cancellation error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sites", "cancelled")); !os.IsNotExist(err) {
		t.Fatalf("canceled Create left a site behind: %v", err)
	}
}
