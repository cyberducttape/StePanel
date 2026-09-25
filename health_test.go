package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLivezOnlyReportsProcessLiveness(t *testing.T) {
	app := &App{}
	response := httptest.NewRecorder()
	app.livez(response, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
}

func TestReadyzFailsWhenRecoveryIsUnresolved(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "imports"), filepath.Join(root, "backups"), filepath.Join(root, "sites")} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{
		Config: Config{ImportRoot: filepath.Join(root, "imports"), BackupRoot: filepath.Join(root, "backups"), JobState: filepath.Join(root, "jobs.json"), RecoveryRoot: filepath.Join(root, "sites", ".stepanel-recovery"), MinFreeBytes: 1},
		Jobs:   NewJobs(), RecoveryError: errors.New("recovery transaction requires operator action"),
	}
	response := httptest.NewRecorder()
	app.readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "recovery_state") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestReadyzChecksPersistentCapacity(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "imports"), filepath.Join(root, "backups"), filepath.Join(root, "sites")} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{Config: Config{ImportRoot: filepath.Join(root, "imports"), BackupRoot: filepath.Join(root, "backups"), JobState: filepath.Join(root, "jobs.json"), RecoveryRoot: filepath.Join(root, "sites", ".stepanel-recovery"), MinFreeBytes: 1}, Jobs: NewJobs()}
	response := httptest.NewRecorder()
	app.readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	app.Config.BackupRoot = filepath.Join(root, "missing")
	response = httptest.NewRecorder()
	app.readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestOperationalHealthReportsMissingRequiredOffsiteBackup(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "imports"), filepath.Join(root, "backups"), filepath.Join(root, "sites")} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{Config: Config{ImportRoot: filepath.Join(root, "imports"), BackupRoot: filepath.Join(root, "backups"), JobState: filepath.Join(root, "jobs.json"), RecoveryRoot: filepath.Join(root, "sites", ".stepanel-recovery"), MinFreeBytes: 1, RequireOffsiteBackup: true}, Jobs: NewJobs()}
	response := httptest.NewRecorder()
	app.operationalHealth(response, httptest.NewRequest(http.MethodGet, "/api/health/operational", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "offsite_backup") || !strings.Contains(response.Body.String(), `"operational":false`) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestOperationalHealthReportsDurableDeadLetters(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "imports"), filepath.Join(root, "backups"), filepath.Join(root, "sites")} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	db, err := openControlPlaneDB(filepath.Join(root, "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	item := &Job{ID: "dead-letter-test", Kind: "test", State: "dead-letter", User: "site", StartedAt: time.Now().UTC(), Error: "operator review required"}
	jobs.items[item.ID] = item
	if err := jobs.persistLocked(); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{ImportRoot: filepath.Join(root, "imports"), BackupRoot: filepath.Join(root, "backups"), JobState: filepath.Join(root, "jobs.json"), RecoveryRoot: filepath.Join(root, "sites", ".stepanel-recovery"), MinFreeBytes: 1}, Jobs: jobs}
	response := httptest.NewRecorder()
	app.operationalHealth(response, httptest.NewRequest(http.MethodGet, "/api/health/operational", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "dead_letter_jobs") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestRestoreCapacityChecksDestinationFilesystem(t *testing.T) {
	root := t.TempDir()
	imports := filepath.Join(root, "imports")
	if err := os.MkdirAll(imports, 0750); err != nil {
		t.Fatal(err)
	}
	err := restoreCapacity(Config{ImportRoot: imports, WebRoot: filepath.Join(root, "web"), MinFreeBytes: 1})
	if err == nil {
		t.Fatal("missing destination filesystem passed restore capacity check")
	}
}

func TestLoadConfigAppliesCapacityLimits(t *testing.T) {
	t.Setenv("STEPANEL_MAX_UPLOAD_BYTES", "1048576")
	t.Setenv("STEPANEL_MAX_ARCHIVE_ENTRIES", "500")
	t.Setenv("STEPANEL_MAX_CONCURRENT_JOBS", "4")
	cfg := LoadConfig()
	if cfg.MaxUpload != 1048576 || cfg.MaxEntries != 500 || cfg.MaxConcurrentJobs != 4 {
		t.Fatalf("capacity config = upload %d, entries %d, jobs %d", cfg.MaxUpload, cfg.MaxEntries, cfg.MaxConcurrentJobs)
	}
}
