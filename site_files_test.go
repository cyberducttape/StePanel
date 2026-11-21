package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cyberducttape/StePanel/internal/sitefiles"
)

// newSiteFilesApp returns an app whose customer alice owns the placed sites
// alice (public_html) and shop, and whose customer bob owns neither.
func newSiteFilesApp(t *testing.T) (*App, string) {
	t.Helper()
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	cfg := placedSiteConfig(t)
	cfg.AuditLog = filepath.Join(t.TempDir(), "audit.log")
	docRoot := filepath.Join(cfg.HomeRoot, "alice", "public_html")
	if err := os.MkdirAll(docRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.WebRoot, "sites", "legacy", "public"), 0o750); err != nil {
		t.Fatal(err)
	}
	a := &App{Config: cfg, Auth: Auth{Username: "admin"}}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", Plan: "starter", Sites: []string{"alice", "shop", "legacy"}},
		"bob":   {Username: "bob", Plan: "starter", Sites: []string{"bob-site"}},
	}}
	return a, docRoot
}

func siteFilesRequest(a *App, user, method, target string, body []byte, scopes ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	ctx := context.WithValue(r.Context(), apiTokenUsernameKey{}, user)
	if len(scopes) > 0 {
		ctx = context.WithValue(ctx, apiTokenScopesKey{}, scopes)
	}
	w := httptest.NewRecorder()
	a.siteFiles(w, r.WithContext(ctx))
	return w
}

func TestFileStoreRequiresResolvedSiteCapability(t *testing.T) {
	if _, err := resolveAuthorizedFileTarget(AuthorizedSite{}); err == nil {
		t.Fatal("zero-value authorization capability was accepted")
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "admin"))
	resolved, ok := (&App{Auth: Auth{Username: "admin"}, Accounts: &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", Plan: "starter", Sites: []string{"shop"}},
	}}}).authorizeSite(r, "shop")
	if !ok {
		t.Fatal("administrator authorization should resolve the site capability")
	}
	if got, err := resolveAuthorizedFileTarget(resolved); err != nil || got.site != "shop" || got.tenantID != "alice" {
		t.Fatalf("resolveAuthorizedFileTarget() = %+v, %v", got, err)
	}
}

func TestSiteFilesLifecycleInAPlacedSite(t *testing.T) {
	a, docRoot := newSiteFilesApp(t)
	do := func(method, target string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		return siteFilesRequest(a, "alice", method, target, body)
	}
	if w := do(http.MethodPost, "/api/sites/files/alice/mkdir", []byte(`{"path":"assets"}`)); w.Code != http.StatusOK {
		t.Fatalf("mkdir = %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPut, "/api/sites/files/alice/content?path=assets/site.css", []byte("body{}")); w.Code != http.StatusOK {
		t.Fatalf("write = %d %s", w.Code, w.Body)
	}
	if data, err := os.ReadFile(filepath.Join(docRoot, "assets", "site.css")); err != nil || string(data) != "body{}" {
		t.Fatalf("written file = %q, %v", data, err)
	}
	w := do(http.MethodGet, "/api/sites/files/alice?path=assets", nil)
	var listing struct {
		Entries []struct{ Name, Type string } `json:"entries"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &listing) != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "site.css" {
		t.Fatalf("list = %d %s", w.Code, w.Body)
	}
	w = do(http.MethodGet, "/api/sites/files/alice/content?path=assets/site.css", nil)
	var content struct {
		Text    bool   `json:"text"`
		Content string `json:"content"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &content) != nil || !content.Text || content.Content != "body{}" {
		t.Fatalf("read = %d %s", w.Code, w.Body)
	}
	w = do(http.MethodGet, "/api/sites/files/alice/content?path=assets/site.css&download=1", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") || w.Body.String() != "body{}" {
		t.Fatalf("download = %d %v %q", w.Code, w.Header(), w.Body)
	}
	if w := do(http.MethodPost, "/api/sites/files/alice/rename", []byte(`{"path":"assets/site.css","destination":"style.css"}`)); w.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPost, "/api/sites/files/alice/mkdir", []byte(`{"path":"assets"}`)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "already exists") {
		t.Fatalf("second mkdir = %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPost, "/api/sites/files/alice/delete", []byte(`{"path":"assets"}`)); w.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodGet, "/api/sites/files/alice/content?path=assets/site.css", nil); w.Code != http.StatusNotFound {
		t.Fatalf("read after delete = %d %s", w.Code, w.Body)
	}
	audit, err := os.ReadFile(a.Config.AuditLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"site.files.mkdir", "site.files.write", "site.files.rename", "site.files.delete"} {
		if !strings.Contains(string(audit), action) {
			t.Errorf("audit log has no %s record", action)
		}
	}
}

func TestSiteFilesRejectsStaleEditorRevision(t *testing.T) {
	a, docRoot := newSiteFilesApp(t)
	file := filepath.Join(docRoot, "index.php")
	if err := os.WriteFile(file, []byte("version one"), 0o640); err != nil {
		t.Fatal(err)
	}
	read := siteFilesRequest(a, "alice", http.MethodGet, "/api/sites/files/alice/content?path=index.php", nil)
	var response struct {
		Revision string `json:"revision"`
	}
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &response) != nil || !sitefiles.ValidRevision(response.Revision) {
		t.Fatalf("read revision = %d %s", read.Code, read.Body)
	}
	if err := os.WriteFile(file, []byte("version two from another editor"), 0o640); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/sites/files/alice/content?path=index.php", strings.NewReader("stale editor content"))
	request.Header.Set("If-Match", response.Revision)
	ctx := context.WithValue(request.Context(), apiTokenUsernameKey{}, "alice")
	writer := httptest.NewRecorder()
	a.siteFiles(writer, request.WithContext(ctx))
	if writer.Code != http.StatusConflict {
		t.Fatalf("stale edit status = %d %s, want 409", writer.Code, writer.Body)
	}
	if contents, err := os.ReadFile(file); err != nil || string(contents) != "version two from another editor" {
		t.Fatalf("stale edit changed current contents to %q: %v", contents, err)
	}
}

func TestSiteFileReadLimiterBoundsPerTenantAndHost(t *testing.T) {
	limiter := newSiteFileAdmissionLimiter(2, 3)
	first, ok := limiter.acquire("tenant-a")
	if !ok {
		t.Fatal("first tenant-a read was rejected")
	}
	second, ok := limiter.acquire("tenant-a")
	if !ok {
		t.Fatal("second tenant-a read was rejected")
	}
	if release, ok := limiter.acquire("tenant-a"); ok {
		release()
		t.Fatal("tenant-a exceeded its per-tenant read limit")
	}
	third, ok := limiter.acquire("tenant-b")
	if !ok {
		t.Fatal("tenant-b was blocked by tenant-a's per-tenant limit")
	}
	if release, ok := limiter.acquire("tenant-c"); ok {
		release()
		t.Fatal("host-wide read limit was exceeded")
	}
	first()
	first() // Release closures are idempotent.
	fourth, ok := limiter.acquire("tenant-c")
	if !ok {
		t.Fatal("released host capacity was not reusable")
	}
	second()
	third()
	fourth()
	if limiter.total != 0 || len(limiter.active) != 0 {
		t.Fatalf("released reads left limiter state: total=%d tenants=%d", limiter.total, len(limiter.active))
	}
	if _, ok := limiter.acquire(""); ok {
		t.Fatal("empty tenant identity was admitted")
	}
}

func TestSiteFileWriteLimiterPreventsSingleTenantMonopoly(t *testing.T) {
	limiter := newSiteFileAdmissionLimiter(maxConcurrentSiteFileWritesPerTenant, maxConcurrentSiteFileWritesHost)
	releases := make([]func(), 0, maxConcurrentSiteFileWritesHost)
	for i := 0; i < maxConcurrentSiteFileWritesPerTenant; i++ {
		release, ok := limiter.acquire("tenant-a")
		if !ok {
			t.Fatal("tenant A was rejected before reaching its write limit")
		}
		releases = append(releases, release)
	}
	if release, ok := limiter.acquire("tenant-a"); ok {
		release()
		t.Fatal("tenant A exceeded its per-tenant write limit")
	}
	if release, ok := limiter.acquire("tenant-b"); !ok {
		t.Fatal("tenant A's uploads monopolized all host write capacity")
	} else {
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
	}
	if limiter.total != 0 || len(limiter.active) != 0 {
		t.Fatalf("write slots leaked: total=%d tenants=%d", limiter.total, len(limiter.active))
	}
}

func TestSiteFileReadLimiterConcurrentAdmissionsNeverExceedTenantLimit(t *testing.T) {
	const requests = 40
	limiter := newSiteFileAdmissionLimiter(4, 32)
	start := make(chan struct{})
	releases := make(chan func(), requests)
	var attempts sync.WaitGroup
	var admitted atomic.Int32
	for i := 0; i < requests; i++ {
		attempts.Add(1)
		go func() {
			defer attempts.Done()
			<-start
			if release, ok := limiter.acquire("same-tenant"); ok {
				admitted.Add(1)
				releases <- release
			}
		}()
	}
	close(start)
	attempts.Wait()
	if got := admitted.Load(); got != 4 {
		t.Fatalf("simultaneous admissions = %d, want exactly four", got)
	}
	close(releases)
	for release := range releases {
		release()
	}
	if limiter.total != 0 || len(limiter.active) != 0 {
		t.Fatalf("concurrent releases left limiter state: total=%d tenants=%d", limiter.total, len(limiter.active))
	}
}

func TestSiteFilesReadLimitReturnsRetryableStatus(t *testing.T) {
	a, _ := newSiteFilesApp(t)
	releases := make([]func(), 0, maxConcurrentSiteFileReadsPerTenant)
	for i := 0; i < maxConcurrentSiteFileReadsPerTenant; i++ {
		release, ok := siteFileReads.acquire("alice")
		if !ok {
			t.Fatal("could not reserve test file-read slots")
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	w := siteFilesRequest(a, "alice", http.MethodGet, "/api/sites/files/shop", nil)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "5" {
		t.Fatalf("file listing under tenant saturation = %d %v %s", w.Code, w.Header(), w.Body)
	}
}

func TestSiteFilesRefusesUnsafeRequests(t *testing.T) {
	a, _ := newSiteFilesApp(t)
	for name, tc := range map[string]struct {
		user, method, target, body string
		scopes                     []string
		want                       int
	}{
		"another tenant's site":     {"bob", http.MethodGet, "/api/sites/files/alice", "", nil, http.StatusForbidden},
		"traversal in a listing":    {"alice", http.MethodGet, "/api/sites/files/alice?path=../../etc", "", nil, http.StatusUnprocessableEntity},
		"traversal in a write":      {"alice", http.MethodPut, "/api/sites/files/alice/content?path=a/../../x", "x", nil, http.StatusUnprocessableEntity},
		"traversal in a rename":     {"alice", http.MethodPost, "/api/sites/files/alice/rename", `{"path":"a","destination":"../a"}`, nil, http.StatusUnprocessableEntity},
		"deleting the root":         {"alice", http.MethodPost, "/api/sites/files/alice/delete", `{"path":""}`, nil, http.StatusUnprocessableEntity},
		"a legacy site":             {"alice", http.MethodGet, "/api/sites/files/legacy", "", nil, http.StatusConflict},
		"a read-only token writing": {"alice", http.MethodPost, "/api/sites/files/alice/mkdir", `{"path":"x"}`, []string{"files:read"}, http.StatusForbidden},
		"a token without files":     {"alice", http.MethodGet, "/api/sites/files/alice", "", []string{"site:read"}, http.StatusForbidden},
		"an unknown operation":      {"alice", http.MethodPost, "/api/sites/files/alice/chmod", `{"path":"x"}`, nil, http.StatusMethodNotAllowed},
	} {
		if w := siteFilesRequest(a, tc.user, tc.method, tc.target, []byte(tc.body), tc.scopes...); w.Code != tc.want {
			t.Errorf("%s = %d %s, want %d", name, w.Code, strings.TrimSpace(w.Body.String()), tc.want)
		}
	}
	// Concurrent uploads are bounded before they consume host resources.
	writeReleases := make([]func(), 0, maxConcurrentSiteFileWritesHost)
	for i := 0; i < maxConcurrentSiteFileWritesHost; i++ {
		release, ok := siteFileWrites.acquire(fmt.Sprintf("write-filler-%d", i))
		if !ok {
			t.Fatal("could not reserve test upload slots")
		}
		writeReleases = append(writeReleases, release)
	}
	w := siteFilesRequest(a, "alice", http.MethodPut, "/api/sites/files/alice/content?path=x", []byte("x"))
	for _, release := range writeReleases {
		release()
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("write with every upload slot busy = %d", w.Code)
	}
	big := bytes.Repeat([]byte("x"), 4<<20+1)
	if w := siteFilesRequest(a, "alice", http.MethodPut, "/api/sites/files/alice/content?path=big", big); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized write = %d", w.Code)
	}
}
