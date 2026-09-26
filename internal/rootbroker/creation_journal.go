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

// Site-creation step journal.
//
// The site-creation job runs a sequence of steps that build up a complete
// site. Before this journal existed, a process crash partway through left
// behind a partially-created site with no on-disk record of exactly which
// steps had committed (site directories initialized, files written, etc.).
//
// The journal is a JSON file at <RecoveryRoot>/site-creation-<jobID>.json.
// It records which step names have committed. On each retry, site creation
// loads the journal, skips every step already listed as complete, and
// rewrites the journal after each new step commits. Roll-forward semantics
// apply: the durable state is the source of truth, and retries drive the
// sequence to completion.
//
// Once every step is complete, the journal file is removed; the audit
// trail becomes the durable record of the creation.

const (
	creationJournalVersion = 1

	stepInitialized     = "INITIALIZED"
	stepPersisted       = "PERSISTED"
	stepPHPConfigured   = "PHP_CONFIGURED"
	stepDatabaseCreated = "DATABASE_CREATED"
	stepVhostCreated    = "VHOST_CREATED"
	stepCompleted       = "COMPLETED"
)

// creationStepOrder is the canonical execution order. Every step must be
// idempotent so that retries are safe.
var creationStepOrder = []string{
	stepInitialized,
	stepPersisted,
	stepPHPConfigured,
	stepDatabaseCreated,
	stepVhostCreated,
	stepCompleted,
}

type creationJournal struct {
	Version   int             `json:"version"`
	JobID     string          `json:"job_id"`
	Site      string          `json:"site"`
	Actor     string          `json:"actor"`
	StartedAt time.Time       `json:"started_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Completed map[string]bool `json:"completed"`

	path string // journal file path (not serialized)
}

// creationJournalPath is the on-disk location for a job's creation journal.
func creationJournalPath(recoveryRoot, jobID string) (string, error) {
	if strings.TrimSpace(recoveryRoot) == "" {
		return "", errors.New("recovery root is empty")
	}
	if !validJobIDForJournal(jobID) {
		return "", fmt.Errorf("invalid job id for creation journal: %q", jobID)
	}
	return filepath.Join(recoveryRoot, "site-creation-"+jobID+".json"), nil
}

// validJobIDForJournal restricts the job id to a safe character set.
// Path is constructed by filepath.Join, so anything containing a path
// separator would escape RecoveryRoot; enforce the tighter alphabet up front.
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

// loadOrCreateCreationJournal reads an existing journal for jobID or
// creates a new one. A caller that receives a fresh journal has
// zero completed steps recorded.
func loadOrCreateCreationJournal(recoveryRoot, jobID, site, actor string) (*creationJournal, error) {
	path, err := creationJournalPath(recoveryRoot, jobID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(recoveryRoot, 0700); err != nil {
		return nil, fmt.Errorf("prepare recovery root: %w", err)
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var journal creationJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return nil, fmt.Errorf("decode creation journal %s: %w", filepath.Base(path), err)
		}
		if journal.Version != creationJournalVersion || journal.JobID != jobID || journal.Site != site {
			return nil, fmt.Errorf("creation journal for job %s does not match request", jobID)
		}
		if journal.Completed == nil {
			journal.Completed = map[string]bool{}
		}
		journal.path = path
		return &journal, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read creation journal: %w", err)
	}
	now := time.Now().UTC()
	return &creationJournal{
		Version:   creationJournalVersion,
		JobID:     jobID,
		Site:      site,
		Actor:     actor,
		StartedAt: now,
		UpdatedAt: now,
		Completed: map[string]bool{},
		path:      path,
	}, nil
}

func (j *creationJournal) isComplete(step string) bool {
	if j == nil {
		return false
	}
	return j.Completed[step]
}

// markComplete records that step has committed and persists the journal
// atomically. Callers MUST have finished the step's work before calling
// this — a partial write followed by a crash would leave the journal
// claiming completion for a step that didn't finish.
func (j *creationJournal) markComplete(step string) error {
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
func (j *creationJournal) cleanup() error {
	if j == nil || j.path == "" {
		return nil
	}
	if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove creation journal: %w", err)
	}
	return nil
}

// hasAnyPersistence returns true if any durable state has been written.
// Used to determine if a failed creation should be rolled back or retried.
func (j *creationJournal) hasAnyPersistence() bool {
	if j == nil {
		return false
	}
	return j.isComplete(stepPersisted) || j.isComplete(stepPHPConfigured) ||
		j.isComplete(stepDatabaseCreated) || j.isComplete(stepVhostCreated)
}

// writeAtomicBroker performs an atomic write: write to temp file, then rename.
// This ensures that if the process crashes during the write, the original
// file remains intact (or doesn't exist if it was the first write).
func writeAtomicBroker(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir for atomic write: %w", err)
	}

	// Write to temporary file
	tmpFile, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write to temp file: %w", err)
	}

	if err := tmpFile.Chmod(perm); err != nil {
		tmpFile.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpFile.Name(), path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}
