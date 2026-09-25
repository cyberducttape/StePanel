package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Site-termination step journal.
//
// The site-termination job runs a fixed sequence of destructive steps
// against host state. Before this journal existed, a process crash
// partway through the sequence left behind a partially-torn-down site
// with no on-disk record of exactly which steps had committed. Retries
// often worked (most steps are individually idempotent), but "usually
// idempotent" is not a recovery contract — a retry could re-run a step
// that had already committed and hit a different error, or skip a step
// that had never actually run.
//
// The journal is a JSON file at <RecoveryRoot>/site-termination-<jobID>.json.
// It records which step names have committed. On each retry
// handleSiteTermination loads the journal, skips every step already
// listed as complete, and rewrites the journal after each new step
// commits. Roll-forward semantics apply once BACKUP_VERIFIED completes:
// the verified backup is the durable protection, and every subsequent
// step is a destructive-and-idempotent operation that we drive to
// completion rather than trying to un-do.
//
// Once every step is complete, the journal file is removed; the audit
// trail becomes the durable record of the termination.

const (
	terminationJournalVersion = 1

	stepBackupVerified   = "BACKUP_VERIFIED"
	stepDatabasesRemoved = "DATABASES_REMOVED"
	stepRoutesRemoved    = "ROUTES_REMOVED"
	stepProxiesRemoved   = "PROXIES_REMOVED"
	stepTasksRemoved     = "TASKS_REMOVED"
	stepServicesRemoved  = "SERVICES_REMOVED"
	stepSiteStateRemoved = "SITE_STATE_REMOVED"
	stepOwnershipRemoved = "OWNERSHIP_REMOVED"
)

// terminationStepOrder is the canonical execution order. Kept as a slice
// so tests can assert every step is reachable and so a rename here fails
// loudly at build time if a caller uses a stale constant elsewhere.
var terminationStepOrder = []string{
	stepBackupVerified,
	stepDatabasesRemoved,
	stepRoutesRemoved,
	stepProxiesRemoved,
	stepTasksRemoved,
	stepServicesRemoved,
	stepSiteStateRemoved,
	stepOwnershipRemoved,
}

type terminationJournal struct {
	Version    int             `json:"version"`
	JobID      string          `json:"job_id"`
	Site       string          `json:"site"`
	Actor      string          `json:"actor"`
	StartedAt  time.Time       `json:"started_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Completed  map[string]bool `json:"completed"`
	BackupPath string          `json:"backup_path,omitempty"`

	path string // journal file path (not serialized)
}

// terminationJournalPath is the on-disk location for a job's journal.
// Journal files live under RecoveryRoot alongside SiteTransaction
// journals so a single startup pass can inspect all in-flight
// destructive operations.
func terminationJournalPath(recoveryRoot, jobID string) (string, error) {
	if strings.TrimSpace(recoveryRoot) == "" {
		return "", errors.New("recovery root is empty")
	}
	if !validJobIDForJournal(jobID) {
		return "", fmt.Errorf("invalid job id for termination journal: %q", jobID)
	}
	return filepath.Join(recoveryRoot, "site-termination-"+jobID+".json"), nil
}

// validJobIDForJournal restricts the job id to the character set the
// durable-job layer produces (see newJobID). Path is constructed by
// filepath.Join, so anything containing a path separator or "." would
// escape RecoveryRoot; enforce the tighter alphabet up front.
func validJobIDForJournal(jobID string) bool {
	if jobID == "" || len(jobID) > 128 {
		return false
	}
	for _, r := range jobID {
		if r == '-' || r == '_' || r == ':' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	// Reject any traversal or dot-file pattern that the character
	// allowance above technically permits.
	if strings.Contains(jobID, "..") || strings.HasPrefix(jobID, ".") {
		return false
	}
	return true
}

// loadOrCreateTerminationJournal reads an existing journal for jobID or
// creates a new one. A caller that receives a fresh journal has
// zero completed steps recorded.
func loadOrCreateTerminationJournal(recoveryRoot, jobID, site, actor string) (*terminationJournal, error) {
	path, err := terminationJournalPath(recoveryRoot, jobID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(recoveryRoot, 0700); err != nil {
		return nil, fmt.Errorf("prepare recovery root: %w", err)
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var journal terminationJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return nil, fmt.Errorf("decode termination journal %s: %w", filepath.Base(path), err)
		}
		if journal.Version != terminationJournalVersion || journal.JobID != jobID || journal.Site != site {
			return nil, fmt.Errorf("termination journal for job %s does not match request", jobID)
		}
		if journal.Completed == nil {
			journal.Completed = map[string]bool{}
		}
		journal.path = path
		return &journal, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read termination journal: %w", err)
	}
	now := time.Now().UTC()
	return &terminationJournal{
		Version:   terminationJournalVersion,
		JobID:     jobID,
		Site:      site,
		Actor:     actor,
		StartedAt: now,
		UpdatedAt: now,
		Completed: map[string]bool{},
		path:      path,
	}, nil
}

func (j *terminationJournal) isComplete(step string) bool {
	if j == nil {
		return false
	}
	return j.Completed[step]
}

// markComplete records that step has committed and persists the journal
// atomically. Callers MUST have finished the step's destructive work
// before calling this — a partial write followed by a crash would leave
// the journal claiming completion for a step that didn't finish.
func (j *terminationJournal) markComplete(step string) error {
	if j == nil {
		return errors.New("nil journal")
	}
	if j.Completed == nil {
		j.Completed = map[string]bool{}
	}
	j.Completed[step] = true
	j.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(j.path, append(data, '\n'), 0600)
}

// setBackupPath records the verified backup path in the journal so a
// retry can report the same backup without recreating it. Persistence
// happens as part of the next markComplete call.
func (j *terminationJournal) setBackupPath(path string) {
	if j == nil {
		return
	}
	j.BackupPath = path
}

// cleanup removes the journal after every step is complete. A missing
// file is a no-op — a caller invoking cleanup on a fresh journal that
// never wrote to disk (edge case in test setup) does not fail.
func (j *terminationJournal) cleanup() error {
	if j == nil || j.path == "" {
		return nil
	}
	if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove termination journal: %w", err)
	}
	return nil
}
