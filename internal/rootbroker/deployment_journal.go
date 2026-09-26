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

// App-deployment step journal.
//
// App deployment updates a site's application code with rollback support.
// Each step is idempotent: extracting to a staging location, activating
// the same version, verifying responsiveness are all safe to retry.
//
// The journal is a JSON file at <RecoveryRoot>/app-deployment-<jobID>.json.
// It records which deployment steps have committed. On each retry,
// deployment loads the journal, skips every step already listed as complete,
// and rewrites the journal after each new step commits.

const (
	deploymentJournalVersion = 1

	stepAppValidated     = "APP_VALIDATED"
	stepAppExtracted     = "APP_EXTRACTED"
	stepRollbackTargeted = "ROLLBACK_TARGETED"
	stepAppActivated     = "APP_ACTIVATED"
	stepAppVerified      = "APP_VERIFIED"
	stepMetadataUpdated  = "METADATA_UPDATED"
)

var deploymentStepOrder = []string{
	stepAppValidated,
	stepAppExtracted,
	stepRollbackTargeted,
	stepAppActivated,
	stepAppVerified,
	stepMetadataUpdated,
}

type deploymentJournal struct {
	Version         int             `json:"version"`
	JobID           string          `json:"job_id"`
	Site            string          `json:"site"`
	ReleaseID       string          `json:"release_id"`
	Actor           string          `json:"actor"`
	StartedAt       time.Time       `json:"started_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Completed       map[string]bool `json:"completed"`
	RollbackPath    string          `json:"rollback_path,omitempty"`
	StagingLocation string          `json:"staging_location,omitempty"`

	path string // journal file path (not serialized)
}

// deploymentJournalPath is the on-disk location for a job's deployment journal.
func deploymentJournalPath(recoveryRoot, jobID string) (string, error) {
	if strings.TrimSpace(recoveryRoot) == "" {
		return "", errors.New("recovery root is empty")
	}
	if !validJobIDForJournal(jobID) {
		return "", fmt.Errorf("invalid job id for deployment journal: %q", jobID)
	}
	return filepath.Join(recoveryRoot, "app-deployment-"+jobID+".json"), nil
}

// loadOrCreateDeploymentJournal reads an existing journal for jobID or
// creates a new one. A caller that receives a fresh journal has
// zero completed steps recorded.
func loadOrCreateDeploymentJournal(recoveryRoot, jobID, site, releaseID, actor string) (*deploymentJournal, error) {
	path, err := deploymentJournalPath(recoveryRoot, jobID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(recoveryRoot, 0700); err != nil {
		return nil, fmt.Errorf("prepare recovery root: %w", err)
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var journal deploymentJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return nil, fmt.Errorf("decode deployment journal %s: %w", filepath.Base(path), err)
		}
		if journal.Version != deploymentJournalVersion || journal.JobID != jobID || journal.Site != site {
			return nil, fmt.Errorf("deployment journal for job %s does not match request", jobID)
		}
		if journal.Completed == nil {
			journal.Completed = map[string]bool{}
		}
		journal.path = path
		return &journal, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read deployment journal: %w", err)
	}
	now := time.Now().UTC()
	return &deploymentJournal{
		Version:   deploymentJournalVersion,
		JobID:     jobID,
		Site:      site,
		ReleaseID: releaseID,
		Actor:     actor,
		StartedAt: now,
		UpdatedAt: now,
		Completed: map[string]bool{},
		path:      path,
	}, nil
}

func (j *deploymentJournal) isComplete(step string) bool {
	if j == nil {
		return false
	}
	return j.Completed[step]
}

// markComplete records that step has committed and persists the journal
// atomically. Callers MUST have finished the step's work before calling
// this — a partial write followed by a crash would leave the journal
// claiming completion for a step that didn't finish.
func (j *deploymentJournal) markComplete(step string) error {
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
func (j *deploymentJournal) cleanup() error {
	if j == nil || j.path == "" {
		return nil
	}
	if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove deployment journal: %w", err)
	}
	return nil
}

// setRollbackPath records the location of the previous app version
// for rollback support. This is called before activating the new version.
func (j *deploymentJournal) setRollbackPath(path string) {
	if j == nil {
		return
	}
	j.RollbackPath = path
}

// setStagingLocation records where the new app is extracted.
func (j *deploymentJournal) setStagingLocation(path string) {
	if j == nil {
		return
	}
	j.StagingLocation = path
}
