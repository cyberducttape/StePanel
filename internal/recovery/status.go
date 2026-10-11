package recovery

import (
	"fmt"
	"time"
)

// Confidence summarizes whether a site can be recovered, from strongest to
// weakest evidence.
type Confidence string

const (
	// ConfidenceVerified: a rehearsal passed recently and nothing undermines it.
	ConfidenceVerified Confidence = "verified"
	// ConfidenceDegraded: recovery was proven, but something has since
	// weakened it (stale proof, old backup, missing offsite copy).
	ConfidenceDegraded Confidence = "degraded"
	// ConfidenceFailing: the most recent rehearsal failed.
	ConfidenceFailing Confidence = "failing"
	// ConfidenceUnverified: backups exist but none has been rehearsed.
	ConfidenceUnverified Confidence = "unverified"
	// ConfidenceNoBackup: there is nothing to recover from.
	ConfidenceNoBackup Confidence = "no_backup"
)

// BackupFacts describes the site's newest backup.
type BackupFacts struct {
	Name        string
	CreatedAt   time.Time
	Signed      bool
	Encrypted   bool
	Consistency string
}

// OffsiteFacts describes the newest backup's offsite copy.
type OffsiteFacts struct {
	Configured bool
	// Tracked is false for backups taken before offsite tracking existed.
	Tracked    bool
	UploadedAt *time.Time
}

// Policy sets the ages beyond which evidence is considered stale. A zero
// duration disables that check.
type Policy struct {
	RehearsalMaxAge time.Duration
	BackupMaxAge    time.Duration
}

// Inputs is everything Assess needs; it performs no I/O.
type Inputs struct {
	Site            string
	Now             time.Time
	LatestBackup    *BackupFacts
	Offsite         OffsiteFacts
	LatestRehearsal *Rehearsal
	LatestPassed    *Rehearsal
	Policy          Policy
}

type BackupStatus struct {
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	AgeSeconds int64     `json:"age_seconds"`
	Integrity  string    `json:"integrity"`
	Encrypted  bool      `json:"encrypted"`
}

type OffsiteStatus struct {
	// State is verified, pending, untracked, or not_configured.
	State      string     `json:"state"`
	UploadedAt *time.Time `json:"uploaded_at,omitempty"`
}

type RehearsalStatus struct {
	At               time.Time `json:"at"`
	Backup           string    `json:"backup"`
	Outcome          Outcome   `json:"outcome"`
	Trigger          Trigger   `json:"trigger"`
	Level            Level     `json:"level"`
	LevelDescription string    `json:"level_description"`
	DurationMS       int64     `json:"duration_ms"`
	Error            string    `json:"error,omitempty"`
}

// EvidenceCheck is a reportable recovery claim. not_tested is deliberately
// distinct from pass: archive validation must not imply application recovery.
type EvidenceCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass, warning, not_tested
	Detail string `json:"detail"`
}

// Status is the per-site recovery picture shown to operators.
type Status struct {
	Site       string     `json:"site"`
	Confidence Confidence `json:"confidence"`
	Summary    string     `json:"summary"`
	// Reasons lists every finding that lowered confidence, most severe first.
	Reasons       []string         `json:"reasons"`
	Checks        []EvidenceCheck  `json:"checks"`
	LastBackup    *BackupStatus    `json:"last_backup,omitempty"`
	Offsite       OffsiteStatus    `json:"offsite"`
	LastRehearsal *RehearsalStatus `json:"last_rehearsal,omitempty"`
	LastPassed    *RehearsalStatus `json:"last_passed_rehearsal,omitempty"`
	// MeasuredRecoveryMS is how long the last passing rehearsal took to
	// reach its Level; it is an RTO only for what that level covers.
	MeasuredRecoveryMS int64 `json:"measured_recovery_ms"`
	// RecoveryPointAgeSeconds is how much recent change would be lost if
	// the site were restored from its newest backup right now.
	RecoveryPointAgeSeconds int64 `json:"recovery_point_age_seconds,omitempty"`
	// EncryptionKey is healthy when a passing rehearsal decrypted an
	// encrypted backup with the current key, otherwise unproven.
	EncryptionKey string `json:"encryption_key"`
}

func rehearsalStatus(r *Rehearsal) *RehearsalStatus {
	if r == nil {
		return nil
	}
	return &RehearsalStatus{At: r.FinishedAt, Backup: r.Backup, Outcome: r.Outcome, Trigger: r.Trigger, Level: r.Level, LevelDescription: r.Level.Describe(), DurationMS: r.DurationMS, Error: r.Error}
}

func humanAge(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit
		}
		return fmt.Sprintf("%d %ss", n, unit)
	}
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 48*time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return plural(int(d.Hours()/24), "day")
	}
}

// Assess derives the recovery status from the evidence.
func Assess(in Inputs) Status {
	s := Status{
		Site:    in.Site,
		Reasons: []string{},
		Checks: []EvidenceCheck{
			{Name: "archive", Status: "not_tested", Detail: "No backup has been validated."},
			{Name: "database", Status: "not_tested", Detail: "Database import has not been proven."},
			{Name: "application", Status: "not_tested", Detail: "Application startup and HTTP health have not been proven."},
			{Name: "offsite", Status: "not_tested", Detail: "Offsite durability is not configured."},
		},
		LastRehearsal: rehearsalStatus(in.LatestRehearsal),
		LastPassed:    rehearsalStatus(in.LatestPassed),
		EncryptionKey: "unproven",
	}

	switch {
	case !in.Offsite.Configured:
		s.Offsite.State = "not_configured"
	case in.LatestBackup == nil:
		s.Offsite.State = "pending"
	case !in.Offsite.Tracked:
		s.Offsite.State = "untracked"
	case in.Offsite.UploadedAt != nil:
		s.Offsite.State = "verified"
		s.Offsite.UploadedAt = in.Offsite.UploadedAt
	default:
		s.Offsite.State = "pending"
	}

	if in.LatestBackup == nil {
		s.Confidence = ConfidenceNoBackup
		s.Summary = "No backup exists; this site cannot be recovered."
		s.Reasons = append(s.Reasons, "No backup has been taken.")
		return s
	}
	s.Checks[0] = EvidenceCheck{Name: "archive", Status: "pass", Detail: "A backup manifest and archive are present and validated."}
	if !in.LatestBackup.Signed {
		s.Checks[0] = EvidenceCheck{Name: "archive", Status: "warning", Detail: "A backup is present, but its manifest is unsigned."}
	}
	backupAge := in.Now.Sub(in.LatestBackup.CreatedAt)
	integrity := "verified"
	if !in.LatestBackup.Signed {
		integrity = "unsigned"
	}
	s.LastBackup = &BackupStatus{Name: in.LatestBackup.Name, CreatedAt: in.LatestBackup.CreatedAt, AgeSeconds: int64(backupAge.Seconds()), Integrity: integrity, Encrypted: in.LatestBackup.Encrypted}
	s.RecoveryPointAgeSeconds = int64(backupAge.Seconds())
	if s.Offsite.State == "verified" {
		s.Checks[3] = EvidenceCheck{Name: "offsite", Status: "pass", Detail: "The newest backup has a confirmed offsite copy."}
	} else if s.Offsite.State == "pending" || s.Offsite.State == "untracked" {
		s.Checks[3] = EvidenceCheck{Name: "offsite", Status: "warning", Detail: "The newest backup has no confirmed offsite copy."}
	}

	if in.LatestPassed != nil {
		s.MeasuredRecoveryMS = in.LatestPassed.DurationMS
		if in.LatestPassed.Encrypted {
			s.EncryptionKey = "healthy"
		}
		if in.LatestPassed.Level == LevelApplication {
			s.Checks[1] = EvidenceCheck{Name: "database", Status: "pass", Detail: "The application recovery proof imported and verified the restored database."}
			s.Checks[2] = EvidenceCheck{Name: "application", Status: "pass", Detail: "The application recovery proof verified service activation and an HTTP response."}
		}
	}

	// The most recent attempt decides failure: an older pass does not mask a
	// newer failure.
	if in.LatestRehearsal != nil && in.LatestRehearsal.Outcome == OutcomeFailed {
		s.Confidence = ConfidenceFailing
		s.Summary = "The most recent restore rehearsal failed; recovery from this site's backups is not proven."
		s.Reasons = append(s.Reasons, fmt.Sprintf("Rehearsal of %s failed: %s", in.LatestRehearsal.Backup, in.LatestRehearsal.Error))
	} else if in.LatestPassed == nil {
		s.Confidence = ConfidenceUnverified
		s.Summary = "Backups exist but no restore rehearsal has proven one can be recovered."
		s.Reasons = append(s.Reasons, "No restore rehearsal has passed.")
	} else {
		s.Confidence = ConfidenceVerified
		s.Summary = fmt.Sprintf("Backup %s was recovered in a rehearsal %s ago.", in.LatestPassed.Backup, humanAge(in.Now.Sub(in.LatestPassed.FinishedAt)))
	}

	degrade := func(reason string) {
		s.Reasons = append(s.Reasons, reason)
		if s.Confidence == ConfidenceVerified {
			s.Confidence = ConfidenceDegraded
			s.Summary = "Recovery was proven, but the evidence has weakened since."
		}
	}
	if in.LatestPassed != nil && in.Policy.RehearsalMaxAge > 0 && in.Now.Sub(in.LatestPassed.FinishedAt) > in.Policy.RehearsalMaxAge {
		degrade(fmt.Sprintf("Last passing rehearsal was %s ago (expected within %s).", humanAge(in.Now.Sub(in.LatestPassed.FinishedAt)), humanAge(in.Policy.RehearsalMaxAge)))
	}
	if in.Policy.BackupMaxAge > 0 && backupAge > in.Policy.BackupMaxAge {
		degrade(fmt.Sprintf("Newest backup is %s old (expected within %s).", humanAge(backupAge), humanAge(in.Policy.BackupMaxAge)))
	}
	if !in.LatestBackup.Signed {
		degrade("Newest backup manifest is not signed.")
	}
	if s.Offsite.State == "pending" {
		degrade("Newest backup has no confirmed offsite copy.")
	}
	if in.LatestPassed != nil && in.LatestPassed.Backup != in.LatestBackup.Name && in.LatestPassed.BackupCreatedAt.Before(in.LatestBackup.CreatedAt) {
		// Informational: the pipeline is proven, but not on the newest data.
		s.Reasons = append(s.Reasons, fmt.Sprintf("Newest backup %s has not been rehearsed yet; the proof is from %s.", in.LatestBackup.Name, in.LatestPassed.Backup))
	}
	return s
}
