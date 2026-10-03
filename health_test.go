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
		Jobs:   NewJobs(),
	}
	app.recovery.set(errors.New("recovery transaction requires operator action"))
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

func TestReadyzRequiresFreshCompatibleExternalWorker(t *testing.T) {
	root := t.TempDir()
	imports, backups, sites := filepath.Join(root, "imports"), filepath.Join(root, "backups"), filepath.Join(root, "sites")
	for _, path := range []string{imports, backups, sites} {
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
	cfg := Config{WorkerMode: "external", ImportRoot: imports, BackupRoot: backups, JobState: filepath.Join(root, "jobs.json"), RecoveryRoot: filepath.Join(sites, ".stepanel-recovery"), MinFreeBytes: 1}
	app := &App{Config: cfg, Jobs: jobs}
	response := httptest.NewRecorder()
	app.readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "durable_worker") {
		t.Fatalf("dead worker readiness status = %d, body = %s", response.Code, response.Body.String())
	}
	if err := jobs.publishWorkerHeartbeat("worker-test", "host-test", 123, time.Now().UTC(), durableWorkerJobKinds, nil); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	app.readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"durable_worker":{"ready":true`) {
		t.Fatalf("live worker readiness status = %d, body = %s", response.Code, response.Body.String())
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

func TestOperationalHealthDoesNotTreatConfiguredOffsiteTargetAsVerified(t *testing.T) {
	previousProbe := probeOffsiteRemote
	probeOffsiteRemote = func(string) error { return errors.New("remote credentials rejected") }
	t.Cleanup(func() { probeOffsiteRemote = previousProbe })
	resetOffsiteProbeCache()
	t.Cleanup(resetOffsiteProbeCache)
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "imports"), filepath.Join(root, "backups"), filepath.Join(root, "sites")} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{Config: Config{ImportRoot: filepath.Join(root, "imports"), BackupRoot: filepath.Join(root, "backups"), JobState: filepath.Join(root, "jobs.json"), RecoveryRoot: filepath.Join(root, "sites", ".stepanel-recovery"), MinFreeBytes: 1, RequireOffsiteBackup: true, OffsiteTarget: "s3:bucket/stepanel"}, Jobs: NewJobs()}
	response := httptest.NewRecorder()
	app.operationalHealth(response, httptest.NewRequest(http.MethodGet, "/api/health/operational", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"offsite_backup":{"ready":false`) || !strings.Contains(response.Body.String(), "locally_validated") {
		t.Fatalf("offsite operational state = %d %s", response.Code, response.Body.String())
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

func TestReadyzRemainsAvailableWithDurableDeadLetters(t *testing.T) {
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
	item := &Job{ID: "readyz-dead-letter-test", Kind: "test", State: "dead-letter", User: "site", StartedAt: time.Now().UTC(), Error: "operator review required"}
	jobs.items[item.ID] = item
	if err := jobs.persistLocked(); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{ImportRoot: filepath.Join(root, "imports"), BackupRoot: filepath.Join(root, "backups"), JobState: filepath.Join(root, "jobs.json"), RecoveryRoot: filepath.Join(root, "sites", ".stepanel-recovery"), MinFreeBytes: 1}, Jobs: jobs}
	response := httptest.NewRecorder()
	app.readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":true`) {
		t.Fatalf("readyz status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestRestoreCapacityChecksDestinationFilesystem(t *testing.T) {
	root := t.TempDir()
	imports := filepath.Join(root, "imports")
	if err := os.MkdirAll(imports, 0750); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ImportRoot: imports, WebRoot: filepath.Join(root, "web"), MinFreeBytes: 1}
	err := admitCapacity(cfg, "WPress restore", archiveUploadDemands(cfg, 1))
	if err == nil {
		t.Fatal("missing destination filesystem passed restore capacity check")
	}
}

func TestRestoreCPMoveCapacityAccountsForExpandedCopies(t *testing.T) {
	root := t.TempDir()
	imports := filepath.Join(root, "imports")
	sites := filepath.Join(root, "web", "sites")
	for _, path := range []string{imports, sites} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err := restoreCPMoveCapacity(Config{ImportRoot: imports, WebRoot: filepath.Join(root, "web"), MinFreeBytes: ^uint64(0)}, 12<<30, 34<<30); err == nil || !strings.Contains(err.Error(), "capacity estimate overflow") {
		t.Fatalf("overflow capacity check error = %v", err)
	}
	if err := restoreCPMoveCapacity(Config{ImportRoot: imports, WebRoot: filepath.Join(root, "web"), MinFreeBytes: 1 << 50}, 12<<30, 34<<30); err == nil || !strings.Contains(err.Error(), "required for this cpmove") {
		t.Fatalf("insufficient capacity check error = %v", err)
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

// TestRecoveryErrorIsSynchronized exercises concurrent persistence failures
// and readiness reads; run with -race to detect unsynchronized access.
func TestRecoveryErrorIsSynchronized(t *testing.T) {
	app := &App{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			app.LogPersistenceFailure("save_test", errors.New("disk full"), "concurrency test")
		}
	}()
	for i := 0; i < 100; i++ {
		_ = app.recovery.get()
	}
	<-done
	if app.recovery.get() == nil {
		t.Fatal("recovery error was not recorded")
	}
}
