package stepanel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSiteUsageReportsAccountQuotaHeadroom(t *testing.T) {
	webRoot := t.TempDir()
	public := filepath.Join(webRoot, "sites", "demo", "public")
	if err := os.MkdirAll(public, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "index.php"), make([]byte, 4096), 0o640); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		profile   ResourceProfile
		wantLimit bool
	}{
		"disk quota":      {ResourceProfile{Site: "demo", Account: "solo", DiskMB: 1, Inodes: 100, FilesystemQuotaState: "enforced"}, true},
		"inode-only":      {ResourceProfile{Site: "demo", Account: "solo", Inodes: 100, FilesystemQuotaState: "enforced"}, false},
		"no quota at all": {ResourceProfile{Site: "demo", Account: "solo", FilesystemQuotaState: "none"}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := &App{Config: Config{WebRoot: webRoot, MaxEntries: 1000}, Auth: Auth{Username: "admin"}}
			a.Accounts = &AccountStore{accounts: map[string]HostingAccount{"solo": {Username: "solo", Sites: []string{"demo"}}}}
			a.Resources = &ResourceStore{values: map[string]ResourceProfile{"demo": tc.profile}}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/api/sites/usage/demo", nil)
			a.siteUsage(w, r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "solo")))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			var body struct {
				Bytes int64 `json:"bytes"`
				Quota *struct {
					LimitBytes *int64 `json:"limit_bytes"`
					Remaining  *int64 `json:"estimated_remaining_bytes"`
					ScopeNote  string `json:"scope_note"`
				} `json:"quota"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Bytes != 4096 || body.Quota == nil || body.Quota.ScopeNote == "" {
				t.Fatalf("body = %s", w.Body.String())
			}
			if tc.wantLimit {
				if body.Quota.LimitBytes == nil || *body.Quota.LimitBytes != 1<<20 || body.Quota.Remaining == nil || *body.Quota.Remaining != 1<<20-4096 {
					t.Fatalf("quota = %s", w.Body.String())
				}
			} else if body.Quota.LimitBytes != nil || body.Quota.Remaining != nil {
				t.Fatalf("quota without disk limit reported headroom: %s", w.Body.String())
			}
		})
	}
}
