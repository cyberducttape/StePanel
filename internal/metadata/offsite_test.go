package metadata

import (
	"database/sql"
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
