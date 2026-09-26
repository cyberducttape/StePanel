package rootbroker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Database-restoration step journal.
//
// Database restoration restores a database from a SQL dump file.
// Each step is idempotent: DROP DATABASE IF EXISTS, CREATE DATABASE,
// importing SQL (with progress tracking), and verification.
//
// The journal is a JSON file at <RecoveryRoot>/db-restoration-<jobID>.json.
// It records which restoration steps have committed. On each retry,
// restoration loads the journal, skips every step already listed as complete,
// and rewrites the journal after each new step commits.

const (
	restorJournalVersion = 1

	stepRestoreDumpValidated   = "DUMP_VALIDATED"
	stepRestoreDatabaseDropped = "DATABASE_DROPPED"
	stepRestoreDatabaseCreated = "DATABASE_CREATED"
	stepRestoreDumpImported    = "DUMP_IMPORTED"
	stepRestoreVerified        = "VERIFIED"
)

var restoreStepOrder = []string{
	stepRestoreDumpValidated,
	stepRestoreDatabaseDropped,
	stepRestoreDatabaseCreated,
	stepRestoreDumpImported,
	stepRestoreVerified,
}

type restorationJournal struct {
	Version       int             `json:"version"`
	JobID         string          `json:"job_id"`
	Site          string          `json:"site"`
	Database      string          `json:"database"`
	DumpFile      string          `json:"dump_file"`
	Actor         string          `json:"actor"`
	StartedAt     time.Time       `json:"started_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Completed     map[string]bool `json:"completed"`
	BytesImported int64           `json:"bytes_imported,omitempty"`
	TotalBytes    int64           `json:"total_bytes,omitempty"`

	path string // journal file path (not serialized)
}

// restorationJournalPath is the on-disk location for a job's restoration journal.
func restorationJournalPath(recoveryRoot, jobID string) (string, error) {
	if strings.TrimSpace(recoveryRoot) == "" {
		return "", errors.New("recovery root is empty")
	}
	if !validJobIDForJournal(jobID) {
		return "", fmt.Errorf("invalid job id for restoration journal: %q", jobID)
	}
	return filepath.Join(recoveryRoot, "db-restoration-"+jobID+".json"), nil
}

// loadOrCreateRestorationJournal reads an existing journal for jobID or
// creates a new one. A caller that receives a fresh journal has
// zero completed steps recorded.
func loadOrCreateRestorationJournal(recoveryRoot, jobID, site, database, dumpFile, actor string) (*restorationJournal, error) {
	path, err := restorationJournalPath(recoveryRoot, jobID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(recoveryRoot, 0700); err != nil {
		return nil, fmt.Errorf("prepare recovery root: %w", err)
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var journal restorationJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return nil, fmt.Errorf("decode restoration journal %s: %w", filepath.Base(path), err)
		}
		if journal.Version != restorJournalVersion || journal.JobID != jobID || journal.Site != site {
			return nil, fmt.Errorf("restoration journal for job %s does not match request", jobID)
		}
		if journal.Completed == nil {
			journal.Completed = map[string]bool{}
		}
		journal.path = path
		return &journal, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read restoration journal: %w", err)
	}
	now := time.Now().UTC()
	return &restorationJournal{
		Version:   restorJournalVersion,
		JobID:     jobID,
		Site:      site,
		Database:  database,
		DumpFile:  dumpFile,
		Actor:     actor,
		StartedAt: now,
		UpdatedAt: now,
		Completed: map[string]bool{},
		path:      path,
	}, nil
}

func (j *restorationJournal) isComplete(step string) bool {
	if j == nil {
		return false
	}
	return j.Completed[step]
}

// markComplete records that step has committed and persists the journal
// atomically. Callers MUST have finished the step's work before calling
// this — a partial write followed by a crash would leave the journal
// claiming completion for a step that didn't finish.
func (j *restorationJournal) markComplete(step string) error {
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
	return writeAtomicBroker(j.path, append(data, '\n'), 0600)
}

// cleanup removes the journal after every step is complete. A missing
// file is a no-op — a caller invoking cleanup on a fresh journal that
// never wrote to disk does not fail.
func (j *restorationJournal) cleanup() error {
	if j == nil || j.path == "" {
		return nil
	}
	if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove restoration journal: %w", err)
	}
	return nil
}

// updateProgress records import progress for large dumps.
// Used to track partial imports in case of failure.
func (j *restorationJournal) updateProgress(bytesImported, totalBytes int64) {
	if j == nil {
		return
	}
	j.BytesImported = bytesImported
	j.TotalBytes = totalBytes
}

// getProgress returns current import progress.
func (j *restorationJournal) getProgress() (bytesImported, totalBytes int64) {
	if j == nil {
		return 0, 0
	}
	return j.BytesImported, j.TotalBytes
}
