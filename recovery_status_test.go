package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/recovery"
)

type recoveryFixture struct {
	app    *App
	db     *sql.DB
	backup string
	dir    string
}

// newRecoveryFixture creates one real backup of site "account" and an App
// whose recovery store lives in a migrated control-plane database.
func newRecoveryFixture(t *testing.T) recoveryFixture {
	t.Helper()
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	backupRoot := filepath.Join(root, "backups")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "backup")
	const signingKey = "recovery-test-backup-signing-key-0123456789"
	created, err := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: backupRoot, BackupSigningKey: signingKey}, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := openControlPlaneDB(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := recovery.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Auth:     Auth{Username: "admin"},
		Jobs:     newJobsWithDB(db, 1),
		Recovery: store,
		Config:   Config{WebRoot: webRoot, BackupRoot: backupRoot, BackupSigningKey: signingKey, ImportRoot: filepath.Join(root, "imports"), RehearsalIntervalHours: 24},
	}
	return recoveryFixture{app: app, db: db, backup: filepath.Base(created.Path), dir: created.Path}
}

func (f recoveryFixture) rehearse(t *testing.T, ctx context.Context, request durableBackupRehearsalRequest) error {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.app.handleBackupRehearsalJob(ctx, Job{ID: "rehearsal-" + request.Backup, Payload: payload})
	return err
}

func (f recoveryFixture) status(t *testing.T) siteRecoveryResponse {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/sites/recovery/account", nil)
	request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, "admin"))
	f.app.siteRecovery(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", response.Code, response.Body.String())
	}
	var status siteRecoveryResponse
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestRecoveryStatusBeforeAnyRehearsal(t *testing.T) {
	f := newRecoveryFixture(t)
	status := f.status(t)
	if status.Confidence != recovery.ConfidenceUnverified || status.LastBackup == nil || status.LastBackup.Name != f.backup || status.LastRehearsal != nil {
		t.Fatalf("status = %+v", status.Status)
	}
	if !status.AutomaticRehearsalsEnabled || status.RehearsalIntervalHours != 24 {
		t.Fatalf("policy fields = %+v", status)
	}
}

func TestRecoveryStatusWithoutBackups(t *testing.T) {
	f := newRecoveryFixture(t)
	if err := os.RemoveAll(f.dir); err != nil {
		t.Fatal(err)
	}
	if status := f.status(t); status.Confidence != recovery.ConfidenceNoBackup {
		t.Fatalf("confidence = %s", status.Confidence)
	}
}

func TestPassingRehearsalIsRecordedAndVerifiesRecovery(t *testing.T) {
	f := newRecoveryFixture(t)
	if err := f.rehearse(t, context.Background(), durableBackupRehearsalRequest{Site: "account", Backup: f.backup, Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	status := f.status(t)
	if status.Confidence != recovery.ConfidenceVerified {
		t.Fatalf("confidence = %s, reasons = %v", status.Confidence, status.Reasons)
	}
	passed := status.LastPassed
	if passed == nil || passed.Backup != f.backup || passed.Trigger != recovery.TriggerManual || passed.Level != recovery.LevelArchive || passed.Outcome != recovery.OutcomePassed {
		t.Fatalf("last passed = %+v", passed)
	}
	if !strings.Contains(passed.LevelDescription, "not yet rehearsed") {
		t.Fatalf("level description overclaims: %q", passed.LevelDescription)
	}
	if len(status.History) != 1 {
		t.Fatalf("history = %d", len(status.History))
	}
	phases := map[string]bool{}
	for _, phase := range status.History[0].Phases {
		phases[phase.Name] = true
	}
	if !phases["verify"] || !phases["extract"] || !phases["validate"] || status.History[0].FilesRestored != 1 {
		t.Fatalf("recorded rehearsal = %+v", status.History[0])
	}
	// The backup is not encrypted, so the current key is not proven.
	if status.EncryptionKey != "unproven" {
		t.Fatalf("encryption key = %s", status.EncryptionKey)
	}
}

func TestUnsignedBackupDegradesVerifiedRecovery(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	backupRoot := filepath.Join(root, "backups")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "backup")
	created, err := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: backupRoot}, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	f := newRecoveryFixture(t)
	f.app.Config.BackupRoot, f.app.Config.BackupSigningKey, f.backup = backupRoot, "", filepath.Base(created.Path)
	if err := f.rehearse(t, context.Background(), durableBackupRehearsalRequest{Site: "account", Backup: f.backup, Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	status := f.status(t)
	if status.Confidence != recovery.ConfidenceDegraded || status.LastBackup.Integrity != "unsigned" {
		t.Fatalf("status = %+v", status.Status)
	}
}

func TestFailingRehearsalIsRecordedAndReported(t *testing.T) {
	f := newRecoveryFixture(t)
	// Corrupt the archive after it was signed: the rehearsal must fail and
	// the failure must reach the recovery status, not only the job log.
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := false
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "manifest") || entry.IsDir() {
			continue
		}
		if err := os.WriteFile(filepath.Join(f.dir, entry.Name()), []byte("not the archive"), 0600); err != nil {
			t.Fatal(err)
		}
		corrupted = true
	}
	if !corrupted {
		t.Fatal("no archive file found to corrupt")
	}
	if err := f.rehearse(t, context.Background(), durableBackupRehearsalRequest{Site: "account", Backup: f.backup, Actor: "admin"}); err == nil {
		t.Fatal("rehearsal of a corrupted archive passed")
	}
	latest, err := f.app.Recovery.Latest(context.Background(), "account")
	if err != nil || latest == nil || latest.Outcome != recovery.OutcomeFailed || latest.Error == "" {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	if latest.DurationMS < 0 || latest.FinishedAt.Before(latest.StartedAt) {
		t.Fatalf("timing = %+v", latest)
	}
}

func TestFailedRehearsalOverridesEarlierPass(t *testing.T) {
	f := newRecoveryFixture(t)
	if err := f.rehearse(t, context.Background(), durableBackupRehearsalRequest{Site: "account", Backup: f.backup, Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Recovery.Record(context.Background(), recovery.Rehearsal{Site: "account", Backup: f.backup, Trigger: recovery.TriggerScheduled, Level: recovery.LevelArchive, Outcome: recovery.OutcomeFailed, StartedAt: time.Now().Add(time.Second), FinishedAt: time.Now().Add(2 * time.Second), Error: "extract verified backup: unexpected EOF"}); err != nil {
		t.Fatal(err)
	}
	status := f.status(t)
	if status.Confidence != recovery.ConfidenceFailing || !strings.Contains(strings.Join(status.Reasons, " "), "unexpected EOF") {
		t.Fatalf("status = %+v", status.Status)
	}
	if status.LastPassed == nil {
		t.Fatal("earlier pass should still be reported for context")
	}
}

func TestCancelledRehearsalIsNotRecordedAsRecoveryFailure(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := f.rehearse(t, ctx, durableBackupRehearsalRequest{Site: "account", Backup: f.backup, Actor: "admin"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if latest, err := f.app.Recovery.Latest(context.Background(), "account"); err != nil || latest != nil {
		t.Fatalf("cancelled rehearsal was recorded: %+v %v", latest, err)
	}
}

func TestRehearsalThatCannotBeRecordedFails(t *testing.T) {
	f := newRecoveryFixture(t)
	if _, err := f.db.Exec(`DROP TABLE recovery_rehearsals`); err != nil {
		t.Fatal(err)
	}
	err := f.rehearse(t, context.Background(), durableBackupRehearsalRequest{Site: "account", Backup: f.backup, Actor: "admin"})
	if err == nil || !strings.Contains(err.Error(), "could not be recorded") {
		t.Fatalf("err = %v, want recording failure", err)
	}
}

func TestScheduledBackupEnqueuesRehearsalOncePerInterval(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	f.app.maybeEnqueueScheduledRehearsal(ctx, "account", f.backup)
	jobs := f.app.Jobs.List(10)
	if len(jobs) != 1 || jobs[0].Kind != "backup.rehearsal" {
		t.Fatalf("jobs = %+v", jobs)
	}
	var request durableBackupRehearsalRequest
	if err := json.Unmarshal(jobs[0].Payload, &request); err != nil {
		t.Fatal(err)
	}
	if !request.Scheduled || request.Actor != "scheduler" || request.Backup != f.backup {
		t.Fatalf("scheduled request = %+v", request)
	}

	// The scheduler actor has no account; scheduled rehearsals must still be
	// authorized and are recorded as scheduled.
	if err := f.rehearse(t, ctx, request); err != nil {
		t.Fatal(err)
	}
	latest, _ := f.app.Recovery.Latest(ctx, "account")
	if latest == nil || latest.Trigger != recovery.TriggerScheduled {
		t.Fatalf("latest = %+v", latest)
	}

	// A second scheduled backup within the interval does not queue another.
	f.app.maybeEnqueueScheduledRehearsal(ctx, "account", "a-newer-backup")
	if jobs := f.app.Jobs.List(10); len(jobs) != 1 {
		t.Fatalf("rehearsal re-queued within interval: %d jobs", len(jobs))
	}
}

func TestAutomaticRehearsalsCanBeDisabled(t *testing.T) {
	f := newRecoveryFixture(t)
	f.app.Config.RehearsalIntervalHours = 0
	f.app.maybeEnqueueScheduledRehearsal(context.Background(), "account", f.backup)
	if jobs := f.app.Jobs.List(10); len(jobs) != 0 {
		t.Fatalf("rehearsal queued while disabled: %+v", jobs)
	}
	if status := f.status(t); status.AutomaticRehearsalsEnabled {
		t.Fatal("status reports automatic rehearsals while disabled")
	}
}

func TestRehearsalIntervalConfig(t *testing.T) {
	t.Setenv("STEPANEL_REHEARSAL_INTERVAL_HOURS", "0")
	if got := LoadConfig().RehearsalIntervalHours; got != 0 {
		t.Fatalf("interval = %d, want 0 (disabled)", got)
	}
	t.Setenv("STEPANEL_REHEARSAL_INTERVAL_HOURS", "not-a-number")
	if got := LoadConfig().RehearsalIntervalHours; got != 24 {
		t.Fatalf("interval = %d, want default 24", got)
	}
}
