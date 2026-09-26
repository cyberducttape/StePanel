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

// Database-provisioning step journal.
//
// Database provisioning creates a new database with user and credentials.
// Each step is idempotent: CREATE DATABASE IF NOT EXISTS, CREATE USER IF NOT EXISTS,
// GRANT PRIVILEGES (idempotent), and saving credentials (overwrite safe).
//
// The journal is a JSON file at <RecoveryRoot>/db-provisioning-<jobID>.json.
// It records which provisioning steps have committed. On each retry,
// provisioning loads the journal, skips every step already listed as complete,
// and rewrites the journal after each new step commits.

const (
	dbJournalVersion = 1

	stepDBCreated    = "DB_CREATED"
	stepUserCreated  = "USER_CREATED"
	stepPrivsGranted = "PRIVS_GRANTED"
	stepCredsSaved   = "CREDS_SAVED"
	stepConnVerified = "CONN_VERIFIED"
)

var dbProvisioningStepOrder = []string{
	stepDBCreated,
	stepUserCreated,
	stepPrivsGranted,
	stepCredsSaved,
	stepConnVerified,
}

type databaseJournal struct {
	Version      int             `json:"version"`
	JobID        string          `json:"job_id"`
	Site         string          `json:"site"`
	Database     string          `json:"database"`
	Username     string          `json:"username"`
	Actor        string          `json:"actor"`
	StartedAt    time.Time       `json:"started_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	Completed    map[string]bool `json:"completed"`
	CredLocation string          `json:"cred_location,omitempty"`

	path string // journal file path (not serialized)
}

// databaseJournalPath is the on-disk location for a job's database provisioning journal.
func databaseJournalPath(recoveryRoot, jobID string) (string, error) {
	if strings.TrimSpace(recoveryRoot) == "" {
		return "", errors.New("recovery root is empty")
	}
	if !validJobIDForJournal(jobID) {
		return "", fmt.Errorf("invalid job id for database journal: %q", jobID)
	}
	return filepath.Join(recoveryRoot, "db-provisioning-"+jobID+".json"), nil
}

// loadOrCreateDatabaseJournal reads an existing journal for jobID or
// creates a new one. A caller that receives a fresh journal has
// zero completed steps recorded.
func loadOrCreateDatabaseJournal(recoveryRoot, jobID, site, database, username, actor string) (*databaseJournal, error) {
	path, err := databaseJournalPath(recoveryRoot, jobID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(recoveryRoot, 0700); err != nil {
		return nil, fmt.Errorf("prepare recovery root: %w", err)
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var journal databaseJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return nil, fmt.Errorf("decode database journal %s: %w", filepath.Base(path), err)
		}
		if journal.Version != dbJournalVersion || journal.JobID != jobID || journal.Site != site {
			return nil, fmt.Errorf("database journal for job %s does not match request", jobID)
		}
		if journal.Completed == nil {
			journal.Completed = map[string]bool{}
		}
		journal.path = path
		return &journal, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read database journal: %w", err)
	}
	now := time.Now().UTC()
	return &databaseJournal{
		Version:   dbJournalVersion,
		JobID:     jobID,
		Site:      site,
		Database:  database,
		Username:  username,
		Actor:     actor,
		StartedAt: now,
		UpdatedAt: now,
		Completed: map[string]bool{},
		path:      path,
	}, nil
}

func (j *databaseJournal) isComplete(step string) bool {
	if j == nil {
		return false
	}
	return j.Completed[step]
}

// markComplete records that step has committed and persists the journal
// atomically. Callers MUST have finished the step's work before calling
// this — a partial write followed by a crash would leave the journal
// claiming completion for a step that didn't finish.
func (j *databaseJournal) markComplete(step string) error {
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
func (j *databaseJournal) cleanup() error {
	if j == nil || j.path == "" {
		return nil
	}
	if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove database journal: %w", err)
	}
	return nil
}

// setCredLocation records where database credentials are stored.
func (j *databaseJournal) setCredLocation(path string) {
	if j == nil {
		return
	}
	j.CredLocation = path
}
