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
	"sync"
	"syscall"
	"testing"
	"time"
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
	preview, err := app.buildGitRollbackPreview(ctx, "example", true)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ContentIntegrity != releaseIntegrityUnrecorded {
		t.Fatalf("release retired before digests: %q, want unrecorded", preview.ContentIntegrity)
	}
	app.recordRetiredRelease(ctx, "example", previousRoot)
	preview, err = app.buildGitRollbackPreview(ctx, "example", true)
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
	modified, err := app.buildGitRollbackPreview(ctx, "example", true)
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
		// Deployed releases carry no Git metadata, so metadata that appears
		// later is covered like any other content.
		func() error { return os.Mkdir(filepath.Join(root, ".git"), 0750) },
		func() error { return os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]"), 0640) },
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
	if _, _, found, err := app.releaseDigests.lookup("example", filepath.Base(previousRoot)); err != nil || found {
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
	preview, err := app.buildGitRollbackPreview(context.Background(), "example", true)
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

// Ownership and the permission bits the site helper normalizes are not
// content: changing them keeps the digest.
func TestReleaseContentDigestIgnoresNormalizedPermissions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "a"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	base, _, _, err := gitReleaseContentDigest(ctx, root, 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "dir"), 0750|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "dir", "a"), 0660); err != nil {
		t.Fatal(err)
	}
	if next, _, _, err := gitReleaseContentDigest(ctx, root, 1<<20, 100); err != nil || next != base {
		t.Fatalf("normalized permissions changed the digest: %v", err)
	}
}

// A retained release gaining .git metadata after it was retired is
// reported as modified, and the rollback is refused.
func TestRetainedReleaseGitMetadataIsModification(t *testing.T) {
	app, previousRoot := newReleaseDigestFixture(t)
	app.recordRetiredRelease(context.Background(), "example", previousRoot)
	if err := os.MkdirAll(filepath.Join(previousRoot, ".git"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previousRoot, ".git", "config"), []byte("[core]\n"), 0640); err != nil {
		t.Fatal(err)
	}
	preview, err := app.buildGitRollbackPreview(context.Background(), "example", true)
	if err != nil || preview.ContentIntegrity != releaseIntegrityModified {
		t.Fatalf("retained release with added .git: %q, %v; want modified", preview.ContentIntegrity, err)
	}
	if code, body := postRollback(t, app, preview, false); code != http.StatusConflict || !strings.Contains(body, "modified") {
		t.Fatalf("rollback: %d %s, want 409 modified", code, body)
	}
}

func postRollback(t *testing.T, app *App, preview gitRollbackPreview, acceptUnverified bool) (int, string) {
	t.Helper()
	body, _ := json.Marshal(gitRollbackRequest{Site: preview.Site, Confirm: "ROLLBACK " + preview.Site, PreviewToken: preview.PreviewToken, AcceptUnverified: acceptUnverified})
	response := httptest.NewRecorder()
	app.gitRollback(response, httptest.NewRequest(http.MethodPost, "/api/sites/git-rollback", bytes.NewReader(body)))
	return response.Code, response.Body.String()
}

// A release without a baseline is activated only with the operator's
// explicit override. A release retired before digests is "unrecorded"; one
// whose hashing failed at retirement is "unverified", with the reason.
func TestRollbackToUnverifiedReleaseNeedsOverride(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		retire       func(app *App, previousRoot string)
	}{
		{"legacy", releaseIntegrityUnrecorded, func(*App, string) {}},
		{"recording failed", releaseIntegrityUnverified, func(app *App, previousRoot string) {
			limit := app.Config.MaxEntries
			app.Config.MaxEntries = 1
			app.recordRetiredRelease(context.Background(), "example", previousRoot)
			app.Config.MaxEntries = limit
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, previousRoot := newReleaseDigestFixture(t)
			publicRoot := filepath.Join(filepath.Dir(previousRoot), "public")
			if err := os.WriteFile(filepath.Join(publicRoot, "index.php"), []byte("current"), 0640); err != nil {
				t.Fatal(err)
			}
			tc.retire(app, previousRoot)
			preview, err := app.buildGitRollbackPreview(context.Background(), "example", true)
			if err != nil || preview.ContentIntegrity != tc.status || preview.ContentIntegrityDetail == "" {
				t.Fatalf("preview: %q (%q), %v; want %s with a reason", preview.ContentIntegrity, preview.ContentIntegrityDetail, err, tc.status)
			}
			if tc.status == releaseIntegrityUnverified && !strings.Contains(preview.ContentIntegrityDetail, "entry limit") {
				t.Fatalf("unverified detail does not carry the recording failure: %q", preview.ContentIntegrityDetail)
			}
			if code, body := postRollback(t, app, preview, false); code != http.StatusConflict || !strings.Contains(body, "accept_unverified") {
				t.Fatalf("rollback without override: %d %s, want 409", code, body)
			}
			if data, _ := os.ReadFile(filepath.Join(publicRoot, "index.php")); string(data) != "current" {
				t.Fatalf("refused rollback changed the active release: %q", data)
			}
			code, body := postRollback(t, app, preview, true)
			if code != http.StatusOK || !strings.Contains(body, `"content_integrity":"`+tc.status+`"`) {
				t.Fatalf("rollback with override: %d %s", code, body)
			}
			if data, _ := os.ReadFile(filepath.Join(publicRoot, "index.php")); string(data) != "<?php echo 'v1';" {
				t.Fatalf("overridden rollback did not activate the release: %q", data)
			}
		})
	}
}

// A verified rollback activates a copy, not the retained tree: the retained
// release is consumed, and the live tree is a different directory.
func TestVerifiedRollbackActivatesACopy(t *testing.T) {
	app, previousRoot := newReleaseDigestFixture(t)
	publicRoot := filepath.Join(filepath.Dir(previousRoot), "public")
	app.recordRetiredRelease(context.Background(), "example", previousRoot)
	retained, err := os.Stat(previousRoot)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := app.buildGitRollbackPreview(context.Background(), "example", true)
	if err != nil || preview.ContentIntegrity != releaseIntegrityVerified {
		t.Fatalf("preview: %q, %v", preview.ContentIntegrity, err)
	}
	if code, body := postRollback(t, app, preview, false); code != http.StatusOK || !strings.Contains(body, `"content_integrity":"verified"`) {
		t.Fatalf("verified rollback: %d %s", code, body)
	}
	live, err := os.Stat(publicRoot)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(live, retained) {
		t.Fatal("the retained tree itself was activated")
	}
	if _, err := os.Lstat(previousRoot); !os.IsNotExist(err) {
		t.Fatalf("consumed retained release still present: %v", err)
	}
	// The copy is readable by the active release's group and writable only
	// by the panel until the site helper seals it.
	group := live.Sys().(*syscall.Stat_t).Gid
	for _, name := range []string{".", "lib", "index.php", "lib/app.php"} {
		info, err := os.Lstat(filepath.Join(publicRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Gid != group || info.IsDir() && info.Mode()&os.ModeSetgid == 0 {
			t.Fatalf("%s copied with mode %v gid %d", name, info.Mode(), info.Sys().(*syscall.Stat_t).Gid)
		}
	}
	for name, want := range map[string]string{"index.php": "<?php echo 'v1';", "lib/app.php": "<?php // twelve bytes"} {
		if data, err := os.ReadFile(filepath.Join(publicRoot, name)); err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v", name, data, err)
		}
	}
}

// The site account keeps rewriting the retained release while the rollback
// runs. Whatever the interleaving, the rollback either refuses or activates
// exactly the bytes recorded at retirement, never the rewritten ones.
func TestRollbackUnderConcurrentRetainedReleaseWrites(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		app, previousRoot := newReleaseDigestFixture(t)
		publicRoot := filepath.Join(filepath.Dir(previousRoot), "public")
		big := strings.Repeat("x", 1<<20)
		if err := os.WriteFile(filepath.Join(previousRoot, "big.bin"), []byte(big), 0640); err != nil {
			t.Fatal(err)
		}
		app.recordRetiredRelease(context.Background(), "example", previousRoot)
		preview, err := app.buildGitRollbackPreview(context.Background(), "example", true)
		if err != nil || preview.ContentIntegrity != releaseIntegrityVerified {
			t.Fatalf("preview: %q, %v", preview.ContentIntegrity, err)
		}
		stop := make(chan struct{})
		var writer sync.WaitGroup
		writer.Add(1)
		go func() {
			defer writer.Done()
			evil := strings.Repeat("y", 1<<20)
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				name := []string{"index.php", "big.bin", "lib/app.php"}[i%3]
				content := map[string]string{"index.php": "<?php evil();   ", "big.bin": evil, "lib/app.php": "<?php // evil!!!!!!!"}[name]
				// In-place rewrites keep the inode and size.
				_ = os.WriteFile(filepath.Join(previousRoot, name), []byte(content), 0640)
				time.Sleep(time.Duration(attempt) * 10 * time.Microsecond)
			}
		}()
		code, body := postRollback(t, app, preview, false)
		close(stop)
		writer.Wait()
		switch code {
		case http.StatusOK:
			for name, want := range map[string]string{"index.php": "<?php echo 'v1';", "big.bin": big, "lib/app.php": "<?php // twelve bytes"} {
				if data, err := os.ReadFile(filepath.Join(publicRoot, name)); err != nil || string(data) != want {
					t.Fatalf("attempt %d: activated %s differs from the recorded release (%d bytes), %v", attempt, name, len(data), err)
				}
			}
		case http.StatusConflict:
			if data, _ := os.ReadFile(filepath.Join(publicRoot, "index.php")); len(data) != 0 {
				t.Fatalf("attempt %d: refused rollback changed the active release: %q", attempt, data)
			}
		default:
			t.Fatalf("attempt %d: rollback %d %s", attempt, code, body)
		}
	}
}

// A write the site account makes to the retained release after the copy
// was verified does not reach the activated release.
func TestRollbackIgnoresWritesAfterVerification(t *testing.T) {
	app, previousRoot := newReleaseDigestFixture(t)
	publicRoot := filepath.Join(filepath.Dir(previousRoot), "public")
	app.recordRetiredRelease(context.Background(), "example", previousRoot)
	preview, err := app.buildGitRollbackPreview(context.Background(), "example", true)
	if err != nil {
		t.Fatal(err)
	}
	rollbackVerifiedHook = func(retained string) {
		if err := os.WriteFile(filepath.Join(retained, "index.php"), []byte("<?php evil();   "), 0640); err != nil {
			t.Error(err)
		}
	}
	defer func() { rollbackVerifiedHook = nil }()
	if code, body := postRollback(t, app, preview, false); code != http.StatusOK {
		t.Fatalf("rollback: %d %s", code, body)
	}
	if data, err := os.ReadFile(filepath.Join(publicRoot, "index.php")); err != nil || string(data) != "<?php echo 'v1';" {
		t.Fatalf("activated release carries a write made after verification: %q, %v", data, err)
	}
}

// Rollback works for a site placed in a hosting account, whose site manager
// only activates stages from the account's staging root.
func TestRollbackOfPlacedSite(t *testing.T) {
	cfg := placedSiteConfig(t)
	cfg.MaxEntries = 100
	home := filepath.Join(cfg.HomeRoot, "alice")
	docRoot := filepath.Join(home, "shop.example.org")
	previous := filepath.Join(home, ".stepanel-previous-shop.example.org-1")
	for _, dir := range []string{docRoot, previous} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(docRoot, "index.html"), []byte("current"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous, "index.html"), []byte("previous"), 0o640); err != nil {
		t.Fatal(err)
	}
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "digests.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	digests, err := newReleaseDigestStore(db)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg, releaseDigests: digests}
	app.recordRetiredRelease(context.Background(), "shop", previous)
	preview, err := app.buildGitRollbackPreview(context.Background(), "shop", true)
	if err != nil || preview.ContentIntegrity != releaseIntegrityVerified {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	if code, body := postRollback(t, app, preview, false); code != http.StatusOK {
		t.Fatalf("placed-site rollback: %d %s", code, body)
	}
	if data, err := os.ReadFile(filepath.Join(docRoot, "index.html")); err != nil || string(data) != "previous" {
		t.Fatalf("placed site after rollback: %q, %v", data, err)
	}
}

// Hashing stops at a cancelled context, between reads of a large file.
func TestReleaseContentDigestHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big"), make([]byte, 4<<20), 0640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := gitReleaseContentDigest(ctx, root, 1<<30, 100); err == nil {
		t.Fatal("a cancelled digest completed")
	}
	releaseDigestSlots <- struct{}{}
	releaseDigestSlots <- struct{}{}
	defer func() { <-releaseDigestSlots; <-releaseDigestSlots }()
	waiting, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if _, _, _, err := gitReleaseContentDigest(waiting, root, 1<<30, 100); err == nil {
		t.Fatal("a digest ran beyond the host's concurrent digest slots")
	}
}
