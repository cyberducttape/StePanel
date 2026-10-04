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

// stagingRestoreApp builds an App with fake helpers. The vhost helper records
// its arguments and fails when failVhost is set, standing in for a route that
// cannot be activated.
func stagingRestoreApp(t *testing.T, f interruptionFixture, failVhost bool) (*App, string) {
	t.Helper()
	log := filepath.Join(f.root, "vhostctl.log")
	exit := "0"
	if failVhost {
		exit = "1"
	}
	vhostctl := filepath.Join(f.root, "fake-vhostctl")
	if err := os.WriteFile(vhostctl, []byte("#!/usr/bin/env bash\necho \"$@\" >> \""+log+"\"\nexit "+exit+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	sitectl := filepath.Join(f.root, "fake-sitectl")
	if err := os.WriteFile(sitectl, []byte("#!/usr/bin/env bash\necho \"$@\" >> \""+filepath.Join(f.root, "sitectl.log")+"\"\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg
	cfg.VHostCtl, cfg.SiteCtl = vhostctl, sitectl
	return &App{Config: cfg, Auth: Auth{Username: "admin"}}, log
}

func postStagingRestore(t *testing.T, app *App, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/backups/restore-to-staging", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, "admin"))
	response := httptest.NewRecorder()
	app.backupRestoreToStaging(response, request)
	return response
}

func TestRestoreToStagingPathRejectsUnsafeSiteBeforeFilesystemLookup(t *testing.T) {
	app := &App{}
	request := httptest.NewRequest(http.MethodPost, "/api/backups/restore-to-staging", nil)
	response := httptest.NewRecorder()
	app.backupRestoreToStagingPath(response, request, RestoreToStagingRequest{
		Site:   "../outside",
		Backup: "backup",
		Domain: "staging.example.org",
	}, filepath.Join(t.TempDir(), "backup"))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsafe staging site returned HTTP %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

// Restore-to-staging publishes the backup as a separate, non-indexed site with
// its own route, and never touches the production site.
func TestRestoreToStagingPublishesIsolatedCopy(t *testing.T) {
	f := newInterruptionFixture(t)
	f.writeSite(t, versionOne)
	backup := f.backup(t)
	f.writeSite(t, versionTwo)
	app, vhostLog := stagingRestoreApp(t, f, false)

	response := postStagingRestore(t, app, `{"site":"example-staging","backup":"`+backup+`","domain":"staging.example.org"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("staging restore = %d %s", response.Code, response.Body.String())
	}
	f.assertSite(t, versionTwo)
	staging := interruptionFixture{cfg: f.cfg, site: AuthorizedSite{site: "example-staging"}, root: f.root}
	staging.assertSite(t, versionOne)
	if _, err := os.Stat(filepath.Join(f.cfg.WebRoot, "sites", "example-staging", ".stepanel-staging-noindex")); err != nil {
		t.Fatalf("staging site is missing its noindex marker: %v", err)
	}
	calls, _ := os.ReadFile(vhostLog)
	if strings.TrimSpace(string(calls)) != "apply example-staging staging.example.org" {
		t.Fatalf("route helper calls = %q", calls)
	}
	f.assertQuiescent(t)

	// The same staging site cannot be overwritten by a second restore.
	if again := postStagingRestore(t, app, `{"site":"example-staging","backup":"`+backup+`","domain":"staging.example.org"}`); again.Code != http.StatusConflict {
		t.Fatalf("second restore into an existing staging site = %d, want 409", again.Code)
	}
}

// A staging restore whose route cannot be activated leaves no staging site
// behind and does not touch production, so it can simply be retried.
func TestRestoreToStagingRouteFailureLeavesNoHalfSite(t *testing.T) {
	f := newInterruptionFixture(t)
	f.writeSite(t, versionOne)
	backup := f.backup(t)
	f.writeSite(t, versionTwo)
	app, _ := stagingRestoreApp(t, f, true)

	response := postStagingRestore(t, app, `{"site":"example-staging","backup":"`+backup+`","domain":"staging.example.org"}`)
	if response.Code == http.StatusAccepted {
		t.Fatal("staging restore with a failed route reported success")
	}
	f.assertSite(t, versionTwo)
	if _, err := os.Stat(filepath.Join(f.cfg.WebRoot, "sites", "example-staging")); !os.IsNotExist(err) {
		t.Fatalf("staging site directory remains after a failed restore: %v", err)
	}
	calls, _ := os.ReadFile(filepath.Join(f.root, "sitectl.log"))
	if !strings.Contains(string(calls), "prepare-root example-staging") || !strings.Contains(string(calls), "delete example-staging") {
		t.Fatalf("site helper calls = %q, want prepare then delete of the staging identity", calls)
	}
	f.assertQuiescent(t)

	retry, _ := stagingRestoreApp(t, f, false)
	if again := postStagingRestore(t, retry, `{"site":"example-staging","backup":"`+backup+`","domain":"staging.example.org"}`); again.Code != http.StatusAccepted {
		t.Fatalf("retry after failed staging restore = %d %s", again.Code, again.Body.String())
	}
}
