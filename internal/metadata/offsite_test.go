package metadata

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOffsiteBackupSummaryTracksReplicationAndRestore(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	index, err := NewBackupIndex(db)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := index.TrackOffsiteBackup("s3:bucket/panel", "example", "backup-1", created); err != nil {
		t.Fatal(err)
	}
	summary, err := index.OffsiteSummary("s3:bucket/panel")
	if err != nil {
		t.Fatal(err)
	}
	if summary.TrackedBackups != 1 || summary.OldestUnreplicated == nil || !summary.OldestUnreplicated.Equal(created) {
		t.Fatalf("pending summary = %+v", summary)
	}
	uploaded := time.Now().UTC().Truncate(time.Second)
	if err := index.MarkOffsiteUploaded("s3:bucket/panel", "example", "backup-1", uploaded); err != nil {
		t.Fatal(err)
	}
	restored := uploaded.Add(time.Minute)
	if err := index.MarkOffsiteRestoreVerified("s3:bucket/panel", "example", "backup-1", restored); err != nil {
		t.Fatal(err)
	}
	summary, err = index.OffsiteSummary("s3:bucket/panel")
	if err != nil {
		t.Fatal(err)
	}
	if summary.OldestUnreplicated != nil || summary.LastSuccessfulBackup == nil || !summary.LastSuccessfulBackup.Equal(uploaded) || summary.LastVerifiedRestore == nil || !summary.LastVerifiedRestore.Equal(restored) {
		t.Fatalf("replicated summary = %+v", summary)
	}
	other, err := index.OffsiteSummary("s3:other")
	if err != nil {
		t.Fatal(err)
	}
	if other.TrackedBackups != 0 || other.LastSuccessfulBackup != nil {
		t.Fatalf("target state leaked: %+v", other)
	}
}

func TestReindexFilesystemIndexesSiteBackupPaths(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	index, err := NewBackupIndex(db)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manifestDir := filepath.Join(root, "example.com", "backup-2026-10-02")
	if err := os.MkdirAll(manifestDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "manifest.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := index.AddBackup(BackupEntry{Site: "example.com", Backup: "backup-2026-10-02", Path: manifestDir, CreatedAt: time.Now().UTC(), VerifiedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := index.ReindexFilesystem(root); err != nil {
		t.Fatal(err)
	}
	var lastIndexed string
	if err := db.QueryRow("SELECT last_indexed FROM backup_index WHERE site = ? AND backup_name = ?", "example.com", "backup-2026-10-02").Scan(&lastIndexed); err != nil {
		t.Fatal(err)
	}
	if lastIndexed == "1970-01-01" {
		t.Fatalf("backup was not reindexed: last_indexed = %q", lastIndexed)
	}
}
