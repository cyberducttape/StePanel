package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitAllowedHostsRejectsUnsafeValues(t *testing.T) {
	for _, value := range []string{"", "localhost", "127.0.0.1", "github.com:443", "github.com/evil"} {
		if err := validateGitAllowedHosts(value); err == nil {
			t.Fatalf("allowed unsafe Git host list %q", value)
		}
	}
	if err := validateGitAllowedHosts("github.com,git.example.net"); err != nil {
		t.Fatalf("rejected valid Git hosts: %v", err)
	}
	if !gitHostAllowed("github.com,git.example.net", "GIT.EXAMPLE.NET") || gitHostAllowed("github.com", "evil.github.com") {
		t.Fatal("Git host allowlist did not use exact case-insensitive matching")
	}
}

func TestGitRollbackAtomicallySwapsPreviousRelease(t *testing.T) {
	webRoot := filepath.Join(t.TempDir(), "www")
	siteRoot := filepath.Join(webRoot, "sites", "example")
	publicRoot := filepath.Join(siteRoot, "public")
	previousRoot := filepath.Join(siteRoot, ".stepanel-previous-old")
	if err := os.MkdirAll(publicRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previousRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, "version"), []byte("current"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previousRoot, "version"), []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{WebRoot: webRoot, MaxEntries: 100}}
	request := httptest.NewRequest(http.MethodPost, "/api/sites/git-rollback", strings.NewReader(`{"site":"example","confirm":"ROLLBACK example"}`))
	response := httptest.NewRecorder()
	app.gitRollback(response, request)
	data, err := os.ReadFile(filepath.Join(publicRoot, "version"))
	if response.Code != http.StatusOK || err != nil || string(data) != "previous" {
		t.Fatalf("status = %d, active = %q, error = %v, body = %s", response.Code, data, err, response.Body.String())
	}
	entries, err := os.ReadDir(siteRoot)
	if err != nil {
		t.Fatal(err)
	}
	foundCurrent := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stepanel-previous-") {
			body, readErr := os.ReadFile(filepath.Join(siteRoot, entry.Name(), "version"))
			foundCurrent = foundCurrent || readErr == nil && string(body) == "current"
		}
	}
	if !foundCurrent {
		t.Fatal("rollback did not preserve the replaced active release")
	}
}

func TestValidateGitReleaseRejectsSymlinksAndEntryFloods(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateGitRelease(root, 2); err != nil {
		t.Fatalf("safe release rejected: %v", err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "secret")); err != nil {
		t.Fatal(err)
	}
	if err := validateGitRelease(root, 10); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink release result = %v", err)
	}
	if err := validateGitRelease(root, 1); err == nil || !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("entry-limit result = %v", err)
	}
}

// TestGitDeployWebhookRejectsCrossSiteBody is the regression test for the
// webhook cross-site vulnerability: a valid signature for site-a must not be
// usable to trigger a deploy on site-b by putting site-b in the request body.
// The trusted target lives in the gitWebhookSiteKey context value, which is
// what verifyWebhookSignature actually authorized.
func TestGitDeployWebhookRejectsCrossSiteBody(t *testing.T) {
	app := &App{Config: Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl")}}
	body := strings.NewReader(`{"site":"site-b","repository":"https://github.com/x/x.git","ref":"main"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/sites/git-deploy", body)
	// Simulate what gitWebhook does after signature verification: injects the
	// authenticated site into context. site-a is the URL/HMAC-authenticated
	// target; the body claims site-b.
	r = r.WithContext(context.WithValue(r.Context(), gitWebhookSiteKey{}, "site-a"))
	w := httptest.NewRecorder()
	app.gitDeploy(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site webhook was not refused: status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "different site") {
		t.Errorf("refusal message does not mention the mismatch: %s", w.Body.String())
	}
}

// TestGitDeployWebhookAcceptsMatchingSite ensures the site-mismatch fix does
// not break the normal case (body site matches URL site) or the elided-site
// case (body has no "site" — we substitute the authenticated one). We stop
// short of a full deploy by making the request fail on repository parsing,
// which happens *after* the site-binding check and so proves that check
// passed.
func TestGitDeployWebhookAcceptsMatchingSite(t *testing.T) {
	for _, bodyPayload := range []string{
		`{"site":"site-a","repository":"","ref":"main"}`,
		`{"repository":"","ref":"main"}`,
	} {
		app := &App{Config: Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl"), GitAllowedHosts: "github.com"}}
		r := httptest.NewRequest(http.MethodPost, "/api/sites/git-deploy", strings.NewReader(bodyPayload))
		r = r.WithContext(context.WithValue(r.Context(), gitWebhookSiteKey{}, "site-a"))
		w := httptest.NewRecorder()
		app.gitDeploy(w, r)
		// The site-binding check has passed — we should get further, and fail
		// on repository parsing (empty URL) rather than on site-mismatch.
		if w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "different site") {
			t.Fatalf("matching webhook was refused as cross-site: body=%q response=%s", bodyPayload, w.Body.String())
		}
	}
}

// TestGitDeployWebhookSanitizedBodyFallsThroughToAuthSite documents the
// second-order defense: a body site that fails safeUser normalization (e.g.
// uppercase, invalid characters) is treated as "no site given" — the
// authenticated context site wins. This prevents an attacker from smuggling
// a cross-site attempt through casing/escaping tricks that make our
// equality check see two "different" strings that both fail sanitization.
func TestGitDeployWebhookSanitizedBodyFallsThroughToAuthSite(t *testing.T) {
	app := &App{Config: Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl")}}
	// "SITE-B" fails safeUser (uppercase); it becomes "" and the authenticated
	// "site-a" wins. This is safe because the attacker cannot cause a deploy
	// on any site other than site-a, which is what the signature authorized.
	body := strings.NewReader(`{"site":"SITE-B","repository":"","ref":"main"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/sites/git-deploy", body)
	r = r.WithContext(context.WithValue(r.Context(), gitWebhookSiteKey{}, "site-a"))
	w := httptest.NewRecorder()
	app.gitDeploy(w, r)
	if w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "different site") {
		t.Fatalf("sanitized-body request should not be rejected as cross-site: %s", w.Body.String())
	}
}

func TestMatchRefPatternRejectsNearMatches(t *testing.T) {
	// The old matchGlobPattern stripped '*' and did a substring match, so
	// "refs/heads/release/*" authorized every ref containing the substring
	// "refs/heads/release/" — including unrelated branches. Lock the fix in.
	patterns := []string{"refs/heads/release/*"}
	if !matchRefPattern("refs/heads/release/v1", patterns) {
		t.Error("release/v1 should match refs/heads/release/*")
	}
	// Path.Match's '*' does not cross '/', so a nested branch does NOT match a
	// single-star pattern — users must opt into '**' for recursion.
	if matchRefPattern("refs/heads/release/v1/hotfix", patterns) {
		t.Error("nested ref should not match single-star pattern")
	}
	// The old-bug near-match: 'other-release/main' contains the substring
	// 'release/' but is not authorized.
	if matchRefPattern("refs/heads/other-release/main", patterns) {
		t.Error("substring-only near-match must not be authorized (regression)")
	}
	// Adjacent-branch escape: prefix collision must not match.
	if matchRefPattern("refs/heads/release-secret/main", patterns) {
		t.Error("prefix-collision ref must not match")
	}
}

func TestMatchRefPatternRecursiveGlob(t *testing.T) {
	patterns := []string{"refs/heads/release/**"}
	for _, ref := range []string{"refs/heads/release", "refs/heads/release/v1", "refs/heads/release/v1/hotfix"} {
		if !matchRefPattern(ref, patterns) {
			t.Errorf("recursive pattern should match %q", ref)
		}
	}
	for _, ref := range []string{"refs/heads/main", "refs/heads/other-release/x", "refs/heads/release-secret/x"} {
		if matchRefPattern(ref, patterns) {
			t.Errorf("recursive pattern must not match %q", ref)
		}
	}
}

func TestMatchRefPatternIsCaseSensitive(t *testing.T) {
	// Git refs on case-sensitive filesystems are case-sensitive. A pattern
	// configured as lowercase "refs/heads/main" must not authorize a ref
	// spelled "refs/heads/Main", which would be a distinct ref in Git.
	if matchRefPattern("refs/heads/Main", []string{"refs/heads/main"}) {
		t.Error("ref matching must be case-sensitive")
	}
	if !matchRefPattern("refs/heads/Main", []string{"refs/heads/Main"}) {
		t.Error("exact-case match should succeed")
	}
}

func TestMatchRefPatternBareBranchShorthand(t *testing.T) {
	// Backward compat: a bare-branch pattern authorizes the corresponding
	// refs/heads/ ref. This documents the shortcut so future contributors
	// don't remove it thinking it's dead code.
	if !matchRefPattern("refs/heads/main", []string{"main"}) {
		t.Error("bare 'main' should authorize refs/heads/main")
	}
	if !matchRefPattern("refs/tags/v1.0", []string{"v1.0"}) {
		t.Error("bare 'v1.0' should authorize refs/tags/v1.0")
	}
	// A pattern that is already in refs/... form is anchored — no shortcut.
	// The bare-shorthand path only applies when the pattern doesn't start
	// with "refs/".
	if matchRefPattern("refs/heads/main", []string{"refs/tags/main"}) {
		t.Error("full-ref pattern should not fall back to short-name match")
	}
}

func TestMatchRefPatternExactAndEmpty(t *testing.T) {
	if !matchRefPattern("refs/heads/main", []string{"refs/heads/main"}) {
		t.Error("exact match failed")
	}
	if matchRefPattern("", []string{"refs/heads/main"}) {
		t.Error("empty ref must never match")
	}
	if matchRefPattern("refs/heads/main", nil) {
		t.Error("nil pattern list must never match")
	}
	if matchRefPattern("refs/heads/main", []string{""}) {
		t.Error("empty pattern must never match")
	}
}
