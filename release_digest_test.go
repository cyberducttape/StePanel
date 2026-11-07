package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newReleaseDigestFixture(t *testing.T) (*App, string) {
	t.Helper()
	webRoot := filepath.Join(t.TempDir(), "www")
	siteRoot := filepath.Join(webRoot, "sites", "example")
	previousRoot := filepath.Join(siteRoot, ".stepanel-previous-old")
	for _, root := range []string{filepath.Join(siteRoot, "public"), filepath.Join(previousRoot, "lib")} {
		if err := os.MkdirAll(root, 0750); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"index.php": "<?php echo 'v1';", "lib/app.php": "<?php // twelve bytes"} {
		if err := os.WriteFile(filepath.Join(previousRoot, name), []byte(content), 0640); err != nil {
			t.Fatal(err)
		}
	}
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	digests, err := newReleaseDigestStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return &App{Config: Config{WebRoot: webRoot, MaxEntries: 100}, releaseDigests: digests}, previousRoot
}

// A retained release recorded at retirement verifies while unchanged, and an
// edit that preserves size, mtime and even the metadata fingerprint's other
// inputs is caught by the content digest.
func TestRetainedReleaseContentDigestDetectsEdits(t *testing.T) {
	app, previousRoot := newReleaseDigestFixture(t)
	ctx := context.Background()
	preview, err := app.buildGitRollbackPreview(ctx, "example")
	if err != nil {
		t.Fatal(err)
	}
	if preview.ContentIntegrity != releaseIntegrityUnrecorded {
		t.Fatalf("release retired before digests: %q, want unrecorded", preview.ContentIntegrity)
	}
	app.recordRetiredRelease(ctx, "example", previousRoot)
	preview, err = app.buildGitRollbackPreview(ctx, "example")
	if err != nil || preview.ContentIntegrity != releaseIntegrityVerified {
		t.Fatalf("unchanged retained release: %q, %v; want verified", preview.ContentIntegrity, err)
	}
	target := filepath.Join(previousRoot, "lib", "app.php")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("<?php evil(); //"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(target, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	modified, err := app.buildGitRollbackPreview(ctx, "example")
	if err != nil || modified.ContentIntegrity != releaseIntegrityModified {
		t.Fatalf("edited retained release: %q, %v; want modified", modified.ContentIntegrity, err)
	}
	if modified.PreviewToken == preview.PreviewToken {
		t.Fatal("an edited retained release kept the verified preview token")
	}
}

// Adding, removing or retargeting entries also changes the digest.
func TestReleaseContentDigestCoversStructure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("one"), 0640); err != nil {
		t.Fatal(err)
	}
	base, files, bytes, err := gitReleaseContentDigest(ctx, root, 1<<20, 100)
	if err != nil || files != 1 || bytes != 3 {
		t.Fatalf("digest = %q files=%d bytes=%d, %v", base, files, bytes, err)
	}
	for _, change := range []func() error{
		func() error { return os.Chmod(filepath.Join(root, "a"), 0750) },
		func() error { return os.WriteFile(filepath.Join(root, "b"), nil, 0640) },
		func() error { return os.Symlink("a", filepath.Join(root, "c")) },
	} {
		if err := change(); err != nil {
			t.Fatal(err)
		}
		next, _, _, err := gitReleaseContentDigest(ctx, root, 1<<20, 100)
		if err != nil {
			t.Fatal(err)
		}
		if next == base {
			t.Fatal("a structural change kept the release digest")
		}
		base = next
	}
	if _, _, _, err := gitReleaseContentDigest(ctx, root, 2, 100); err == nil {
		t.Fatal("a release over the byte limit was hashed")
	}
}

// Digests of releases that no longer exist are dropped.
func TestReleaseDigestsForgetMissingReleases(t *testing.T) {
	app, previousRoot := newReleaseDigestFixture(t)
	app.recordRetiredRelease(context.Background(), "example", previousRoot)
	if err := os.RemoveAll(previousRoot); err != nil {
		t.Fatal(err)
	}
	app.forgetMissingReleaseDigests("example")
	if _, found, err := app.releaseDigests.lookup("example", filepath.Base(previousRoot)); err != nil || found {
		t.Fatalf("digest of a removed release survived: found=%v, %v", found, err)
	}
}

// The rollback endpoint refuses a retained release whose contents changed
// after it was retired, and leaves the active release in place.
func TestGitRollbackRefusesModifiedRetainedRelease(t *testing.T) {
	app, previousRoot := newReleaseDigestFixture(t)
	publicRoot := filepath.Join(filepath.Dir(previousRoot), "public")
	if err := os.WriteFile(filepath.Join(publicRoot, "index.php"), []byte("current"), 0640); err != nil {
		t.Fatal(err)
	}
	app.recordRetiredRelease(context.Background(), "example", previousRoot)
	if err := os.WriteFile(filepath.Join(previousRoot, "index.php"), []byte("<?php echo 'v2';"), 0640); err != nil {
		t.Fatal(err)
	}
	preview, err := app.buildGitRollbackPreview(context.Background(), "example")
	if err != nil || preview.ContentIntegrity != releaseIntegrityModified {
		t.Fatalf("preview: %q, %v", preview.ContentIntegrity, err)
	}
	body, _ := json.Marshal(gitRollbackRequest{Site: "example", Confirm: "ROLLBACK example", PreviewToken: preview.PreviewToken})
	response := httptest.NewRecorder()
	app.gitRollback(response, httptest.NewRequest(http.MethodPost, "/api/sites/git-rollback", bytes.NewReader(body)))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "modified") {
		t.Fatalf("rollback to a modified release: %d %s, want 409", response.Code, response.Body.String())
	}
	if data, err := os.ReadFile(filepath.Join(publicRoot, "index.php")); err != nil || string(data) != "current" {
		t.Fatalf("active release changed: %q, %v", data, err)
	}
}
