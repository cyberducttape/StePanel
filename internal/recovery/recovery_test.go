package recovery

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(Schema); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

var base = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func rehearsal(site, backup string, outcome Outcome, at time.Time) Rehearsal {
	return Rehearsal{Site: site, Backup: backup, BackupCreatedAt: at.Add(-time.Hour), Trigger: TriggerScheduled, Level: LevelArchive, Outcome: outcome, StartedAt: at.Add(-time.Minute), FinishedAt: at, DurationMS: 60000, Phases: []Phase{{Name: "verify", DurationMS: 1000}}, Encrypted: true, FilesRestored: 10, BytesRestored: 2048, Databases: []string{"db1"}}
}

func TestStoreRecordsAndReadsLatest(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	if r, err := store.Latest(ctx, "demo"); err != nil || r != nil {
		t.Fatalf("empty latest = %v, %v", r, err)
	}
	pass := rehearsal("demo", "b1", OutcomePassed, base)
	fail := rehearsal("demo", "b2", OutcomeFailed, base.Add(time.Hour))
	fail.Error = "archive checksum mismatch"
	for _, r := range []Rehearsal{pass, fail, rehearsal("other", "x", OutcomePassed, base.Add(2*time.Hour))} {
		if err := store.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := store.Latest(ctx, "demo")
	if err != nil || latest == nil || latest.Backup != "b2" || latest.Outcome != OutcomeFailed || latest.Error != "archive checksum mismatch" {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	passed, err := store.LatestPassed(ctx, "demo")
	if err != nil || passed == nil || passed.Backup != "b1" {
		t.Fatalf("latest passed = %+v, %v", passed, err)
	}
	if !passed.FinishedAt.Equal(base) || passed.FilesRestored != 10 || passed.BytesRestored != 2048 || !passed.Encrypted || len(passed.Phases) != 1 || passed.Databases[0] != "db1" || passed.Level != LevelArchive {
		t.Fatalf("round trip lost fields: %+v", passed)
	}
}

func TestStorePrunesHistoryPerSite(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	for i := 0; i < historyPerSite+5; i++ {
		if err := store.Record(ctx, rehearsal("demo", fmt.Sprintf("b%02d", i), OutcomePassed, base.Add(time.Duration(i)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Record(ctx, rehearsal("other", "keep", OutcomePassed, base)); err != nil {
		t.Fatal(err)
	}
	history, err := store.History(ctx, "demo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != historyPerSite || history[0].Backup != fmt.Sprintf("b%02d", historyPerSite+4) || history[len(history)-1].Backup != "b05" {
		t.Fatalf("history len %d, newest %s, oldest %s", len(history), history[0].Backup, history[len(history)-1].Backup)
	}
	if other, _ := store.History(ctx, "other", 0); len(other) != 1 {
		t.Fatalf("pruning crossed sites: %d", len(other))
	}
}

func TestStoreKeepsUnknownBackupTimeZero(t *testing.T) {
	store := openStore(t)
	early := rehearsal("demo", "b1", OutcomeFailed, base)
	early.BackupCreatedAt = time.Time{}
	early.Error = "manifest signature invalid"
	if err := store.Record(context.Background(), early); err != nil {
		t.Fatal(err)
	}
	got, err := store.Latest(context.Background(), "demo")
	if err != nil || got == nil || !got.BackupCreatedAt.IsZero() {
		t.Fatalf("backup time = %+v, %v", got, err)
	}
}

func TestStoreRejectsInvalidRecords(t *testing.T) {
	store := openStore(t)
	bad := rehearsal("demo", "b1", "maybe", base)
	if err := store.Record(context.Background(), bad); err == nil {
		t.Fatal("invalid outcome accepted")
	}
}

func assessWith(mutate func(*Inputs)) Status {
	latest := &BackupFacts{Name: "b2", CreatedAt: base.Add(-2 * time.Hour), Signed: true, Encrypted: true}
	passed := rehearsal("demo", "b2", OutcomePassed, base.Add(-time.Hour))
	passed.BackupCreatedAt = latest.CreatedAt
	uploaded := base.Add(-90 * time.Minute)
	in := Inputs{
		Site:            "demo",
		Now:             base,
		LatestBackup:    latest,
		Offsite:         OffsiteFacts{Configured: true, Tracked: true, UploadedAt: &uploaded},
		LatestRehearsal: &passed,
		LatestPassed:    &passed,
		Policy:          Policy{RehearsalMaxAge: 48 * time.Hour, BackupMaxAge: 48 * time.Hour},
	}
	mutate(&in)
	return Assess(in)
}

func TestAssessVerified(t *testing.T) {
	s := assessWith(func(*Inputs) {})
	if s.Confidence != ConfidenceVerified || len(s.Reasons) != 0 {
		t.Fatalf("status = %+v", s)
	}
	if s.EncryptionKey != "healthy" || s.MeasuredRecoveryMS != 60000 || s.RecoveryPointAgeSeconds != 7200 || s.Offsite.State != "verified" || s.LastBackup.Integrity != "verified" {
		t.Fatalf("details = %+v", s)
	}
	if !strings.Contains(s.LastPassed.LevelDescription, "not yet rehearsed") {
		t.Fatalf("level description must state its limits: %q", s.LastPassed.LevelDescription)
	}
}

func TestAssessRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Inputs)
		want   Confidence
		reason string
	}{
		{"no backup", func(in *Inputs) { in.LatestBackup = nil }, ConfidenceNoBackup, "No backup"},
		{"never rehearsed", func(in *Inputs) { in.LatestRehearsal, in.LatestPassed = nil, nil }, ConfidenceUnverified, "No restore rehearsal"},
		{"latest failed masks older pass", func(in *Inputs) {
			failed := rehearsal("demo", "b2", OutcomeFailed, base.Add(-10*time.Minute))
			failed.Error = "decrypt failed"
			in.LatestRehearsal = &failed
		}, ConfidenceFailing, "decrypt failed"},
		{"stale rehearsal", func(in *Inputs) {
			old := rehearsal("demo", "b2", OutcomePassed, base.Add(-72*time.Hour))
			in.LatestRehearsal, in.LatestPassed = &old, &old
		}, ConfidenceDegraded, "Last passing rehearsal"},
		{"old backup", func(in *Inputs) { in.LatestBackup.CreatedAt = base.Add(-96 * time.Hour) }, ConfidenceDegraded, "Newest backup is"},
		{"unsigned backup", func(in *Inputs) { in.LatestBackup.Signed = false }, ConfidenceDegraded, "not signed"},
		{"offsite pending", func(in *Inputs) { in.Offsite.UploadedAt = nil }, ConfidenceDegraded, "offsite"},
		{"offsite not configured is not a defect", func(in *Inputs) { in.Offsite = OffsiteFacts{} }, ConfidenceVerified, ""},
		{"offsite untracked legacy backup", func(in *Inputs) { in.Offsite.Tracked = false }, ConfidenceVerified, ""},
		{"policy disabled", func(in *Inputs) {
			in.Policy = Policy{}
			old := rehearsal("demo", "b2", OutcomePassed, base.Add(-720*time.Hour))
			in.LatestRehearsal, in.LatestPassed = &old, &old
		}, ConfidenceVerified, ""},
		{"newest backup not rehearsed is informational", func(in *Inputs) {
			older := rehearsal("demo", "b1", OutcomePassed, base.Add(-time.Hour))
			older.BackupCreatedAt = base.Add(-30 * time.Hour)
			in.LatestRehearsal, in.LatestPassed = &older, &older
		}, ConfidenceVerified, "has not been rehearsed yet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := assessWith(tc.mutate)
			if s.Confidence != tc.want {
				t.Fatalf("confidence = %s, want %s (reasons %v)", s.Confidence, tc.want, s.Reasons)
			}
			if tc.reason == "" && tc.want == ConfidenceVerified && len(s.Reasons) != 0 {
				t.Fatalf("unexpected reasons %v", s.Reasons)
			}
			if tc.reason != "" && !strings.Contains(strings.Join(s.Reasons, " | "), tc.reason) {
				t.Fatalf("reasons %v do not mention %q", s.Reasons, tc.reason)
			}
		})
	}
}

func TestAssessUnencryptedPassDoesNotProveKey(t *testing.T) {
	s := assessWith(func(in *Inputs) {
		plain := *in.LatestPassed
		plain.Encrypted = false
		in.LatestPassed, in.LatestRehearsal = &plain, &plain
	})
	if s.EncryptionKey != "unproven" {
		t.Fatalf("encryption key = %s", s.EncryptionKey)
	}
}

func TestHumanAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second: "under a minute",
		time.Minute:      "1 minute",
		5 * time.Minute:  "5 minutes",
		time.Hour:        "1 hour",
		47 * time.Hour:   "47 hours",
		72 * time.Hour:   "3 days",
	} {
		if got := humanAge(d); got != want {
			t.Errorf("humanAge(%s) = %q, want %q", d, got, want)
		}
	}
}
