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
	for i := 0; i < cap(siteFileWrites); i++ {
		siteFileWrites <- struct{}{}
	}
	w := siteFilesRequest(a, "alice", http.MethodPut, "/api/sites/files/alice/content?path=x", []byte("x"))
	for i := 0; i < cap(siteFileWrites); i++ {
		<-siteFileWrites
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("write with every upload slot busy = %d", w.Code)
	}
	big := bytes.Repeat([]byte("x"), 4<<20+1)
	if w := siteFilesRequest(a, "alice", http.MethodPut, "/api/sites/files/alice/content?path=big", big); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized write = %d", w.Code)
	}
}
