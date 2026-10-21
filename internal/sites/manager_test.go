package sites

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestCreateStagingUsesManagerOwnedRoot(t *testing.T) {
	m, root := newManager(t)
	stage, err := m.CreateStaging(context.Background(), ".stepanel-test-")
	if err != nil {
		t.Fatalf("CreateStaging: %v", err)
	}
	wantParent := filepath.Join(root, "sites", ".stepanel-manager-staging")
	if filepath.Dir(stage) != wantParent {
		t.Fatalf("staging parent = %q, want %q", filepath.Dir(stage), wantParent)
	}
	if err := os.WriteFile(filepath.Join(stage, "index.html"), []byte("staged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ActivateStaged(context.Background(), "managed", stage); err != nil {
		t.Fatalf("ActivateStaged: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "sites", "managed", "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "staged" {
		t.Fatalf("activated content = %q, want staged", data)
	}
}

func TestCreateStagingRejectsUnsafePrefix(t *testing.T) {
	m, _ := newManager(t)
	for _, prefix := range []string{"", "../escape-", ".stepanel/escape-", strings.Repeat("x", 49)} {
		if _, err := m.CreateStaging(context.Background(), prefix); err == nil {
			t.Errorf("CreateStaging(%q) accepted unsafe prefix", prefix)
		}
	}
}

func TestDiscardStagingRemovesOnlyManagerOwnedTree(t *testing.T) {
	m, root := newManager(t)
	stage, err := m.CreateStaging(context.Background(), ".stepanel-test-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "marker"), []byte("temporary"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.DiscardStaging(context.Background(), stage); err != nil {
		t.Fatalf("DiscardStaging: %v", err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staging tree still exists: %v", err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.DiscardStaging(context.Background(), outside); err == nil {
		t.Fatal("DiscardStaging accepted a path outside manager staging")
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatalf("outside sentinel changed: %v", err)
	}
}

func TestReleaseStagingPreservesRunnerLayoutAndOwnership(t *testing.T) {
	m, root := newManager(t)
	public := filepath.Join(root, "sites", "release-site", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	stage, owner, err := m.CreateReleaseStagingLease(context.Background(), "release-site", ".stepanel-release-")
	if err != nil {
		t.Fatalf("CreateReleaseStaging: %v", err)
	}
	defer owner.Close()
	wantParent := filepath.Join(root, "sites", ".stepanel-manager-staging")
	if filepath.Dir(stage) != wantParent || !strings.Contains(filepath.Base(stage), ".stepanel-release-release-site-") {
		t.Fatalf("release staging path = %q, want site-bound staging below %q", stage, wantParent)
	}
	if !IsReleaseStagingName("release-site", filepath.Base(stage)) {
		t.Fatalf("CreateReleaseStaging name %q is not recognised as the site's stage", filepath.Base(stage))
	}
	if err := os.WriteFile(filepath.Join(stage, "version"), []byte("next"), 0600); err != nil {
		t.Fatal(err)
	}
	// Site "release" shares the name prefix and must not discard this stage.
	if err := os.MkdirAll(filepath.Join(root, "sites", "release", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := m.DiscardReleaseStaging(context.Background(), "release", stage); err == nil {
		t.Fatal("DiscardReleaseStaging removed another site's release stage")
	}
	if err := m.DiscardReleaseStaging(context.Background(), "release-site", stage); err != nil {
		t.Fatalf("DiscardReleaseStaging: %v", err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("release staging tree still exists: %v", err)
	}
	if _, _, err := m.CreateReleaseStagingLease(context.Background(), "missing", ".stepanel-release-"); err == nil {
		t.Fatal("CreateReleaseStaging accepted an unmanaged site")
	}
}

func TestCleanupOrphanedReleaseStaging(t *testing.T) {
	m, root := newManager(t)
	for _, site := range []string{"orphan-one", "orphan-two"} {
		public := filepath.Join(root, "sites", site, "public")
		if err := os.MkdirAll(public, 0750); err != nil {
			t.Fatal(err)
		}
		stage, owner, err := m.CreateReleaseStagingLease(context.Background(), site, ".stepanel-release-")
		if err != nil {
			t.Fatalf("CreateReleaseStaging(%s): %v", site, err)
		}
		_ = owner.Close() // Model a process that exited before cleanup.
		if err := os.WriteFile(filepath.Join(stage, "partial-artifact"), []byte("incomplete"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A previous release is not an orphaned checkout and must remain intact.
	previous := filepath.Join(root, "sites", "orphan-one", ".stepanel-previous-release")
	if err := os.MkdirAll(previous, 0750); err != nil {
		t.Fatal(err)
	}
	removed, err := m.CleanupOrphanedStaging(context.Background(), 0)
	if err != nil {
		t.Fatalf("CleanupOrphanedStaging: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	for _, path := range []string{
		filepath.Join(root, "sites", "orphan-one", ".stepanel-previous-release"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained release missing: %v", err)
		}
	}
}

func TestCleanupOrphanedReleaseStagingRequiresOwnershipLease(t *testing.T) {
	m, root := newManager(t)
	legacy := filepath.Join(root, "sites", "legacy", ".stepanel-release-legacy-123")
	if err := os.MkdirAll(legacy, 0750); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(legacy, old, old); err != nil {
		t.Fatal(err)
	}

	removed, err := m.CleanupOrphanedStaging(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("CleanupOrphanedStaging: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed=%d, want legacy stage without ownership evidence to be retained", removed)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy stage was removed without ownership evidence: %v", err)
	}
}

func TestActivateStagedReplacingCanRollback(t *testing.T) {
	m, root := newManager(t)
	public := filepath.Join(root, "sites", "replace-site", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "version"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	stage, owner, err := m.CreateReleaseStagingLease(context.Background(), "replace-site", ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := os.WriteFile(filepath.Join(stage, "version"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	previous, err := m.ActivateStagedReplacing(context.Background(), "replace-site", stage)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(public, "version"))
	if err != nil || string(data) != "new" {
		t.Fatalf("active release = %q, err=%v", data, err)
	}
	if err := m.RollbackStagedActivation(context.Background(), "replace-site", previous); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(public, "version"))
	if err != nil || string(data) != "old" {
		t.Fatalf("rolled back release = %q, err=%v", data, err)
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

// TestDeleteRefusesSymlinkedSite verifies the manager rejects a site entry
// that is already a symlink rather than treating the link as a valid site.
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

func TestDeleteRemainsAnchoredWhenSitesPathIsReplaced(t *testing.T) {
	_, root := newManager(t)
	sitePath := filepath.Join(root, "sites", "victim")
	if err := os.MkdirAll(sitePath, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sitePath, "owned.txt"), []byte("managed site"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(victim, []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}

	openedSites, err := openSitesRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer openedSites.Close()
	if err := os.Rename(filepath.Join(root, "sites"), filepath.Join(root, "sites-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sites")); err != nil {
		t.Fatal(err)
	}
	if err := removeSiteAtRoot(context.Background(), openedSites, "victim"); err != nil {
		t.Fatalf("descriptor-relative delete failed: %v", err)
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "must survive" {
		t.Fatalf("path replacement redirected deletion: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "sites-original", "victim")); !os.IsNotExist(err) {
		t.Fatalf("delete did not operate on the opened sites directory: %v", err)
	}
}

func TestDeleteRejectsWritableSitesParent(t *testing.T) {
	m, root := newManager(t)
	sitePath := filepath.Join(root, "sites", "site")
	if err := os.Mkdir(sitePath, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "sites"), 0770); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(context.Background(), "site"); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("Delete accepted a group-writable sites parent: %v", err)
	}
	if _, err := os.Stat(sitePath); err != nil {
		t.Fatalf("site changed after unsafe parent was rejected: %v", err)
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

func TestDeleteHonorsCancelledContext(t *testing.T) {
	m, root := newManager(t)
	siteRoot := filepath.Join(root, "sites", "cancelled-delete")
	sentinel := filepath.Join(siteRoot, "public", "index.html")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sentinel, []byte("preserve while lock is lost"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Delete(ctx, "cancelled-delete"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete with canceled context error = %v, want context.Canceled", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve while lock is lost" {
		t.Fatalf("cancelled Delete changed the site: data=%q error=%v", data, err)
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

// TestActivateStagedReplacingRestoresPreviousWhenSyncFails injects a parent
// fsync failure after the publishing rename: the staged release must be
// withdrawn and the previous release restored at the canonical path.
func TestActivateStagedReplacingRestoresPreviousWhenSyncFails(t *testing.T) {
	m, root := newManager(t)
	public := filepath.Join(root, "sites", "sync-site", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "version"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	stage, owner, err := m.CreateReleaseStagingLease(context.Background(), "sync-site", ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := os.WriteFile(filepath.Join(stage, "version"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected EIO")
	original := syncDir
	failed := false
	syncDir = func(path string) error {
		if !failed {
			failed = true
			return injected
		}
		return original(path)
	}
	defer func() { syncDir = original }()

	if _, err := m.ActivateStagedReplacing(context.Background(), "sync-site", stage); !errors.Is(err, injected) {
		t.Fatalf("activation error = %v, want injected sync failure", err)
	}
	data, err := os.ReadFile(filepath.Join(public, "version"))
	if err != nil || string(data) != "old" {
		t.Fatalf("active release after failed activation = %q, err=%v", data, err)
	}
}

func TestRestorePreviousReleaseReportsRestoreFailure(t *testing.T) {
	root := t.TempDir()
	cause := errors.New("activation failed")
	err := restorePreviousRelease(filepath.Join(root, "missing-previous"), filepath.Join(root, "public"), cause)
	if !errors.Is(err, cause) {
		t.Fatalf("restore error = %v, want original cause preserved", err)
	}
	if !strings.Contains(err.Error(), "restore previous release") {
		t.Fatalf("restore failure was masked: %v", err)
	}
}

func TestRenameDurableReportsPublishedPathWhenReversalFails(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "stage")
	to := filepath.Join(root, "final")
	if err := os.Mkdir(from, 0700); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected ENOSPC")
	original := syncDir
	syncDir = func(string) error {
		if err := os.MkdirAll(filepath.Join(from, "blocker"), 0700); err != nil {
			t.Fatal(err)
		}
		return injected
	}
	defer func() { syncDir = original }()

	err := renameDurable(from, to)
	if !errors.Is(err, injected) || !strings.Contains(err.Error(), "remains published") {
		t.Fatalf("renameDurable error = %v, want sync failure naming the published path", err)
	}
	if _, statErr := os.Stat(to); statErr != nil {
		t.Fatalf("published path should still exist: %v", statErr)
	}
}

func TestActivateStagedReplacingRefusesUndurableStage(t *testing.T) {
	m, root := newManager(t)
	public := filepath.Join(root, "sites", "durable-site", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "version"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	stage, owner, err := m.CreateReleaseStagingLease(context.Background(), "durable-site", ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	injected := errors.New("injected EIO during data sync")
	original := syncTree
	syncTree = func(context.Context, string) error { return injected }
	defer func() { syncTree = original }()
	if _, err := m.ActivateStagedReplacing(context.Background(), "durable-site", stage); !errors.Is(err, injected) {
		t.Fatalf("activation error = %v, want staged data sync failure", err)
	}
	data, err := os.ReadFile(filepath.Join(public, "version"))
	if err != nil || string(data) != "old" {
		t.Fatalf("current release disturbed by refused activation: %q, %v", data, err)
	}
}

func TestSyncTreeSyncsFilesAndSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "b", "f"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syncTree(context.Background(), root); err != nil {
		t.Fatalf("syncTree: %v", err)
	}
}

func TestCleanupOrphanedStagingSparesTreesStillInUse(t *testing.T) {
	m, root := newManager(t)
	if err := os.MkdirAll(filepath.Join(root, "sites", "demo", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	abandonedRelease, abandonedOwner, err := m.CreateReleaseStagingLease(context.Background(), "demo", ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	_ = abandonedOwner.Close()
	liveRelease, liveOwner, err := m.CreateReleaseStagingLease(context.Background(), "demo", ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	defer liveOwner.Close()
	abandonedStage, err := m.CreateStaging(context.Background(), ".stepanel-backup-")
	if err != nil {
		t.Fatal(err)
	}
	liveStage, err := m.CreateStaging(context.Background(), ".stepanel-backup-")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-4 * time.Hour)
	for _, path := range []string{abandonedRelease, abandonedStage} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := m.CleanupOrphanedStaging(context.Background(), 3*time.Hour)
	if err != nil || removed != 2 {
		t.Fatalf("removed=%d err=%v, want the two abandoned trees", removed, err)
	}
	for path, want := range map[string]bool{abandonedRelease: false, abandonedStage: false, liveRelease: true, liveStage: true} {
		_, err := os.Stat(path)
		if exists := err == nil; exists != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(path), exists, want)
		}
	}
}

func TestCleanupOrphanedStagingHonorsCrossProcessLease(t *testing.T) {
	if stage := os.Getenv("STEPANEL_TEST_RELEASE_LEASE_STAGE"); stage != "" {
		owner, err := os.OpenFile(releaseStageOwnerLockPath(stage), os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Close()
		if err := syscall.Flock(int(owner.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("STEPANEL_TEST_RELEASE_LEASE_READY"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		for {
			if _, err := os.Stat(os.Getenv("STEPANEL_TEST_RELEASE_LEASE_STOP")); err == nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	m, root := newManager(t)
	if err := os.MkdirAll(filepath.Join(root, "sites", "demo", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	stage, owner, err := m.CreateReleaseStagingLease(context.Background(), "demo", ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	_ = owner.Close()
	if _, err := os.Stat(filepath.Join(stage, releaseStageOwnerLock)); !os.IsNotExist(err) {
		t.Fatalf("lease must not be stored in tenant-owned staging tree: %v", err)
	}
	if _, err := os.Stat(releaseStageOwnerLockPath(stage)); err != nil {
		t.Fatalf("panel-owned sibling lease missing: %v", err)
	}
	old := time.Now().Add(-4 * time.Hour)
	if err := os.Chtimes(stage, old, old); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(root, "lease-ready")
	stop := filepath.Join(root, "lease-stop")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCleanupOrphanedStagingHonorsCrossProcessLease$")
	cmd.Env = append(os.Environ(), "STEPANEL_TEST_RELEASE_LEASE_STAGE="+stage,
		"STEPANEL_TEST_RELEASE_LEASE_READY="+ready, "STEPANEL_TEST_RELEASE_LEASE_STOP="+stop)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = os.WriteFile(stop, []byte("stop"), 0600)
			_ = cmd.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease subprocess did not acquire ownership")
		}
		time.Sleep(5 * time.Millisecond)
	}
	removed, err := m.CleanupOrphanedStaging(context.Background(), time.Hour)
	if err != nil || removed != 0 {
		t.Fatalf("cleanup removed=%d err=%v while another process owned stage", removed, err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("active release stage was removed: %v", err)
	}
	if err := os.WriteFile(stop, []byte("stop"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("lease subprocess: %v", err)
	}
	waited = true
	removed, err = m.CleanupOrphanedStaging(context.Background(), time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("cleanup after lease release removed=%d err=%v, want 1", removed, err)
	}
	if _, err := os.Stat(releaseStageOwnerLockPath(stage)); !os.IsNotExist(err) {
		t.Fatalf("stale owner lock was not removed with the stage: %v", err)
	}
}
