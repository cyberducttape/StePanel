package stepanel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagingPromotionRejectsInvalidPairsBeforeLocking(t *testing.T) {
	a := &App{Config: Config{WebRoot: t.TempDir()}}
	for _, handler := range map[string]http.HandlerFunc{"preview": a.stagingPromotionPreview, "promote": a.stagingPromote} {
		for _, body := range []string{`{"source":"","site":"shop"}`, `{"source":"shop-stg","site":""}`, `{"source":"shop","site":"shop"}`, `{"source":"../x","site":"shop"}`} {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest(http.MethodPost, "/api/sites/staging/promote", strings.NewReader(body)))
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: status = %d, want 422", body, w.Code)
			}
		}
	}
}

func TestStagingPromotionRefusesCrossTenantStaging(t *testing.T) {
	a := &App{Config: Config{WebRoot: t.TempDir()}, Auth: Auth{Username: "admin"}}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", Plan: "starter", Sites: []string{"alice-site"}},
		"bob":   {Username: "bob", Plan: "starter", Sites: []string{"bob-stg"}},
	}}
	for name, handler := range map[string]http.HandlerFunc{"preview": a.stagingPromotionPreview, "promote": a.stagingPromote} {
		r := httptest.NewRequest(http.MethodPost, "/api/sites/staging/promote", strings.NewReader(`{"source":"bob-stg","site":"alice-site"}`))
		r = r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "alice"))
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403: %s", name, w.Code, w.Body.String())
		}
	}
}

func TestNewPromotionDatabaseIdentityFitsEngineLimits(t *testing.T) {
	first, firstUser, err := newPromotionDatabaseIdentity("a-very-long-production-site-name")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := newPromotionDatabaseIdentity("a-very-long-production-site-name")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("candidate database identities must be unique per promotion")
	}
	if !validManagedDatabaseIdentifier(first, 64) || !validManagedDatabaseIdentifier(firstUser, 32) || strings.Contains(first, "-") {
		t.Fatalf("identity %q/%q is not a valid managed identifier", first, firstUser)
	}
}

func TestWriteWordPressMaintenanceMarkerReplacesExisting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".maintenance"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeWordPressMaintenanceMarker(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".maintenance"))
	if err != nil || !strings.Contains(string(data), "$upgrading") {
		t.Fatalf("marker = %q, %v", data, err)
	}
}
