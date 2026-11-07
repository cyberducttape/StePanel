package stepanel

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanupBackupStagesRemovesOnlyExpiredTemporaryDirectories(t *testing.T) {
	root := t.TempDir()
	oldStage := filepath.Join(root, ".backup-old")
	newStage := filepath.Join(root, ".backup-new")
	ordinary := filepath.Join(root, "published")
	for _, path := range []string{oldStage, newStage, ordinary} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldStage, old, old); err != nil {
		t.Fatal(err)
	}
	if err := CleanupBackupStages(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldStage); !os.IsNotExist(err) {
		t.Fatalf("expired staging directory still exists: %v", err)
	}
	for _, path := range []string{newStage, ordinary} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("non-expired or published directory changed: %s: %v", path, err)
		}
	}
}

func TestCleanupBackupStagesTreatsMissingRootAsNoOp(t *testing.T) {
	if err := CleanupBackupStages(filepath.Join(t.TempDir(), "missing"), time.Hour); err != nil {
		t.Fatalf("missing backup root cleanup = %v", err)
	}
}

func TestCleanupBackupStagesSkipsOwnedStage(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, ".backup-owned")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireBackupStageLock(stage)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseBackupStageLock(lock)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stage, old, old); err != nil {
		t.Fatal(err)
	}
	if err := CleanupBackupStages(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("active backup stage was removed: %v", err)
	}
}

func TestPruneSiteBackupsKeepsNewestAndOtherSites(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	for _, item := range []struct {
		name      string
		site      string
		createdAt time.Time
	}{
		{"20260101-000000.000000000-demo", "demo", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"20260102-000000.000000000-demo", "demo", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{"20260103-000000.000000000-demo", "demo", time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
		{"20260101-000000.000000000-other", "other", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		dir := filepath.Join(testSiteBackupDirectory(t, backupRoot, item.site), item.name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		manifest := BackupManifest{Version: 1, Site: item.site, Archive: "backup.tar.gz", CreatedAt: item.createdAt}
		if err := writeBackupManifest(dir, manifest); err != nil {
			t.Fatal(err)
		}
	}
	access := AuthorizedSite{site: "demo"}
	if _, err := pruneSiteBackups(backupRoot, access, 2, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "20260101-000000.000000000-demo")); !os.IsNotExist(err) {
		t.Fatal("oldest demo backup was not pruned")
	}
	for _, name := range []string{"20260102-000000.000000000-demo", "20260103-000000.000000000-demo", "20260101-000000.000000000-other"} {
		if _, err := os.Stat(filepath.Join(testSiteBackupDirectory(t, backupRoot, backupNameSite(name)), name)); err != nil {
			t.Fatalf("expected %s to remain: %v", name, err)
		}
	}
}

func TestPruneSiteBackupsUsesManifestCreationTime(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	items := []struct {
		name      string
		createdAt time.Time
	}{
		{name: "renamed-new", createdAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
		{name: "renamed-old", createdAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, item := range items {
		dir := filepath.Join(root, item.name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := writeBackupManifest(dir, BackupManifest{Version: 1, Site: "demo", Archive: "backup.tar.gz", CreatedAt: item.createdAt}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "renamed-new")); err != nil {
		t.Fatalf("newest manifest backup was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "renamed-old")); !os.IsNotExist(err) {
		t.Fatalf("oldest manifest backup remains: %v", err)
	}
}

func TestPruneSiteBackupsFailsClosedOnUnownedCorruptDirectory(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	good := filepath.Join(root, "known-backup")
	if err := os.Mkdir(good, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupManifest(good, BackupManifest{Version: 1, Site: "demo", Archive: "backup.tar.gz", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(root, "damaged-backup")
	if err := os.Mkdir(corrupt, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, "manifest.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, "")
	if err != nil {
		t.Fatalf("an unattributable entry stopped retention: %v", err)
	}
	if len(report.Unattributed) != 1 || report.Unattributed[0] != "damaged-backup" {
		t.Fatalf("retention report = %+v, want the damaged entry reported", report)
	}
	if _, err := os.Stat(corrupt); err != nil {
		t.Fatalf("uncertain artifact was deleted: %v", err)
	}
}

func TestPruneSiteBackupsRefusesUnknownCreationTimeWithoutPruning(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	for _, name := range []string{"known-backup", "unknown-date-backup"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		manifest := BackupManifest{Version: 1, Site: "demo", Archive: "backup.tar.gz"}
		if name == "known-backup" {
			manifest.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		if err := writeBackupManifest(dir, manifest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, "known-backup"); err == nil {
		t.Fatal("retention accepted an unknown backup creation time")
	}
	for _, name := range []string{"known-backup", "unknown-date-backup"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("retention deleted %s despite ambiguous chronology: %v", name, err)
		}
	}
}

func TestPruneSiteBackupsRejectsSymlinkManifestWithoutPruning(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	external := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(external, []byte(`{"site":"demo","created_at":"2026-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "linked-backup")
	if err := os.Mkdir(backup, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(backup, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if report, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, ""); err != nil || len(report.Unattributed) != 1 {
		t.Fatalf("symlinked manifest: report %+v, err %v; want it kept and reported", report, err)
	}
	if _, err := os.Lstat(backup); err != nil {
		t.Fatalf("uncertain backup was removed: %v", err)
	}
}

func TestPruneSiteBackupsProtectsNewlyVerifiedBackupAgainstClockOrdering(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	items := []struct {
		name      string
		createdAt time.Time
	}{
		{name: "verified-current", createdAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "timestamp-newer", createdAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, item := range items {
		dir := filepath.Join(root, item.name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := writeBackupManifest(dir, BackupManifest{Version: 1, Site: "demo", Archive: "backup.tar.gz", CreatedAt: item.createdAt}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, "verified-current"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "verified-current")); err != nil {
		t.Fatalf("newly verified recovery point was pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "timestamp-newer")); !os.IsNotExist(err) {
		t.Fatalf("timestamp-ordered stale point was not pruned: %v", err)
	}
}

func TestPruneSiteBackupsRefusesMissingProtectedVerifiedBackup(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	dir := filepath.Join(root, "other-backup")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupManifest(dir, BackupManifest{Version: 1, Site: "demo", Archive: "backup.tar.gz", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, "missing-verified"); err == nil {
		t.Fatal("retention pruned without finding the just-verified recovery point")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("inventory changed after protected backup was missing: %v", err)
	}
}

// A retention interrupted by a crash leaves its backup as a tombstone, never
// a backup with a missing manifest; the next prune finishes the deletion and
// ignores the tombstone when it inventories backups.
func TestPruneSiteBackupsFinishesAnInterruptedDeletion(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	for _, item := range []struct {
		name      string
		createdAt time.Time
	}{
		{"20260102-000000.000000000-demo", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{"20260103-000000.000000000-demo", time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
	} {
		dir := filepath.Join(root, item.name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := writeBackupManifest(dir, BackupManifest{Version: 1, Site: "demo", Archive: "backup.tar.gz", CreatedAt: item.createdAt}); err != nil {
			t.Fatal(err)
		}
	}
	// What a crash after the rename and part of the delete leaves: the
	// manifest is gone, an archive remains.
	tombstone := filepath.Join(root, backupPruneTombstonePrefix+"20260101-000000.000000000-demo")
	if err := os.Mkdir(tombstone, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tombstone, "backup.tar.gz"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, ""); err != nil {
		t.Fatalf("prune after an interrupted deletion: %v", err)
	}
	if _, err := os.Stat(tombstone); !os.IsNotExist(err) {
		t.Fatalf("tombstone of the interrupted deletion remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "20260102-000000.000000000-demo")); !os.IsNotExist(err) {
		t.Fatal("older backup beyond keep was not pruned")
	}
	if _, err := os.Stat(filepath.Join(root, "20260103-000000.000000000-demo")); err != nil {
		t.Fatalf("newest backup was pruned: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("backup root holds %d entries after prune, want only the kept backup", len(entries))
	}
}

// testSiteBackupDirectory creates and returns site's backup directory.
func testSiteBackupDirectory(t *testing.T, root, site string) string {
	t.Helper()
	dir, err := ensureBackupSiteDirectory(root, site)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeTestBackup publishes a backup in the directory of the site its name
// is for, with a manifest naming site.
func writeTestBackup(t *testing.T, root, name, site string, createdAt time.Time) {
	t.Helper()
	owner := backupNameSite(name)
	if owner == "" {
		owner = site
	}
	dir := filepath.Join(testSiteBackupDirectory(t, root, owner), name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupManifest(dir, BackupManifest{Version: 1, Site: site, Archive: "backup.tar.gz", CreatedAt: createdAt}); err != nil {
		t.Fatal(err)
	}
}

// F-03: another site's damaged backup must not stop this site's retention,
// and must not be deleted by it.
func TestPruneSiteBackupsIgnoresAnotherSitesDamagedBackup(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	day := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	writeTestBackup(t, backupRoot, "20260101-000000.000000000-demo", "demo", day(1))
	writeTestBackup(t, backupRoot, "20260102-000000.000000000-demo", "demo", day(2))
	damaged := filepath.Join(testSiteBackupDirectory(t, backupRoot, "other"), "20260101-000000.000000000-other")
	if err := os.Mkdir(damaged, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(damaged, "manifest.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, "")
	if err != nil {
		t.Fatalf("another site's damaged backup stopped retention: %v", err)
	}
	if len(report.Unattributed) != 0 {
		t.Fatalf("demo's report names another site's entry: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(root, "20260101-000000.000000000-demo")); !os.IsNotExist(err) {
		t.Fatal("demo's oldest backup was not pruned")
	}
	if _, err := os.Stat(damaged); err != nil {
		t.Fatalf("another site's damaged backup was removed: %v", err)
	}
	// The damaged entry is reported by the site it is named for.
	if report, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "other"}, 1, ""); err != nil || len(report.Unattributed) != 1 {
		t.Fatalf("owning site's retention: report %+v, err %v; want the damaged entry reported", report, err)
	}
}

// F-07: retention does not open manifests of backups named for other sites.
func TestPruneSiteBackupsSkipsOtherSitesManifests(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-000 manifest")
	}
	backupRoot := t.TempDir()
	writeTestBackup(t, backupRoot, "20260101-000000.000000000-demo", "demo", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	writeTestBackup(t, backupRoot, "20260101-000000.000000000-other", "other", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	unreadable := filepath.Join(testSiteBackupDirectory(t, backupRoot, "other"), "20260101-000000.000000000-other", "manifest.json")
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0600) })
	if report, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, ""); err != nil || len(report.Unattributed) != 0 {
		t.Fatalf("demo retention touched another site's manifest: report %+v, err %v", report, err)
	}
}

// A backup named for this site whose manifest names another site is kept
// and reported, never pruned as this site's.
func TestPruneSiteBackupsReportsNameManifestMismatch(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	writeTestBackup(t, backupRoot, "20260101-000000.000000000-demo", "other", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	writeTestBackup(t, backupRoot, "20260102-000000.000000000-demo", "demo", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	report, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, "")
	if err != nil || len(report.Unattributed) != 1 || report.Unattributed[0] != "20260101-000000.000000000-demo" {
		t.Fatalf("report %+v, err %v; want the mismatched entry reported", report, err)
	}
	if _, err := os.Stat(filepath.Join(root, "20260101-000000.000000000-demo")); err != nil {
		t.Fatalf("mismatched entry was deleted: %v", err)
	}
}

func TestBackupNameSiteParsesOnlyPublishedNames(t *testing.T) {
	for name, want := range map[string]string{
		"20260101-000000.000000000-demo":    "demo",
		"20260101-000000.000000000-my-shop": "my-shop",
		"renamed-new":                       "",
		"20260101-000000.000000000-":        "",
		"2026010x-000000.000000000-demo":    "",
		"20260101-000000.000000000-Bad/One": "",
	} {
		if got := backupNameSite(name); got != want {
			t.Errorf("backupNameSite(%q) = %q, want %q", name, got, want)
		}
	}
}

// A retention failure is kept with the schedule across restarts and exported
// as a metric, not only audited.
func TestRetentionOutcomeIsPersistedAndExported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	store, err := openBackupSchedules(path)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.items["demo"] = BackupSchedule{Site: "demo", IntervalMinutes: 60, KeepLast: 7, Enabled: true, NextRun: time.Now().UTC().Add(time.Hour)}
	if err := store.persistLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()
	store.recordRetention("demo", backupRetentionReport{Unattributed: []string{"damaged-backup"}}, errors.New("manifest has no created_at"))

	restarted, err := openBackupSchedules(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted.mu.Lock()
	schedule := restarted.items["demo"]
	restarted.mu.Unlock()
	if schedule.RetentionCheckedAt == nil || schedule.RetentionError == "" || len(schedule.RetentionUnattributed) != 1 {
		t.Fatalf("retention outcome was not persisted: %+v", schedule)
	}
	var output strings.Builder
	writeBackupScheduleMetrics(&output, []BackupSchedule{schedule})
	for _, want := range []string{`stepanel_backup_retention_ok{site="demo"} 0`, `stepanel_backup_retention_unattributed_entries{site="demo"} 1`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, output.String())
		}
	}
	restarted.recordRetention("demo", backupRetentionReport{}, nil)
	restarted.mu.Lock()
	schedule = restarted.items["demo"]
	restarted.mu.Unlock()
	if schedule.RetentionError != "" || len(schedule.RetentionUnattributed) != 0 {
		t.Fatalf("a clean retention run did not clear the degraded state: %+v", schedule)
	}
}

// Retention finishes only its own site's interrupted deletions; the startup
// sweep finishes everyone's.
func TestPruneSiteBackupsFinishesOnlyItsOwnTombstones(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	writeTestBackup(t, backupRoot, "20260101-000000.000000000-demo", "demo", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	other := testSiteBackupDirectory(t, backupRoot, "other")
	for _, tombstone := range []string{
		filepath.Join(root, backupPruneTombstonePrefix+"20250101-000000.000000000-demo"),
		filepath.Join(other, backupPruneTombstonePrefix+"20250101-000000.000000000-other"),
		// Left in the backup root by a release before per-site directories.
		filepath.Join(backupRoot, backupPruneTombstonePrefix+"20240101-000000.000000000-other"),
	} {
		if err := os.MkdirAll(filepath.Join(tombstone, "data"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, backupPruneTombstonePrefix+"20250101-000000.000000000-demo")); !os.IsNotExist(err) {
		t.Fatalf("demo's own tombstone was not finished: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, backupPruneTombstonePrefix+"20250101-000000.000000000-other")); err != nil {
		t.Fatalf("demo's retention touched another site's tombstone: %v", err)
	}
	if err := removePruneTombstones(backupRoot); err != nil {
		t.Fatal(err)
	}
	for _, tombstone := range []string{
		filepath.Join(other, backupPruneTombstonePrefix+"20250101-000000.000000000-other"),
		filepath.Join(backupRoot, backupPruneTombstonePrefix+"20240101-000000.000000000-other"),
	} {
		if _, err := os.Stat(tombstone); !os.IsNotExist(err) {
			t.Fatalf("startup sweep left a tombstone %s: %v", tombstone, err)
		}
	}
}

// Every unattributed entry is counted, but the report and its single log
// line carry a bounded number of names.
func TestRetentionReportCountsAllButBoundsNames(t *testing.T) {
	backupRoot := t.TempDir()
	root := testSiteBackupDirectory(t, backupRoot, "demo")
	writeTestBackup(t, backupRoot, "20260101-000000.000000000-demo", "demo", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for i := 0; i < maxReportedUnattributedBackups+5; i++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("odd-entry-%02d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	report, err := pruneSiteBackups(backupRoot, AuthorizedSite{site: "demo"}, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.UnattributedCount != maxReportedUnattributedBackups+5 || len(report.Unattributed) != maxReportedUnattributedBackups {
		t.Fatalf("report counted %d and listed %d", report.UnattributedCount, len(report.Unattributed))
	}
	if lines := strings.Count(strings.TrimSpace(logged.String()), "\n") + 1; lines != 1 || strings.Contains(logged.String(), "odd-entry-24") {
		t.Fatalf("retention logged %d lines (want one bounded line): %s", lines, logged.String())
	}
}
