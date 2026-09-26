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

// Vhost-configuration step journal.
//
// Vhost configuration sets up virtual host configuration in a webserver.
// Each step is idempotent: generating the same config, writing it (overwrite safe),
// applying to webserver, and reloading are all safe to retry.
//
// The journal is a JSON file at <RecoveryRoot>/vhost-config-<jobID>.json.
// It records which configuration steps have committed.

const (
	vhostJournalVersion = 1

	stepVhostValidated    = "VHOST_VALIDATED"
	stepVhostConfigGen    = "CONFIG_GENERATED"
	stepVhostConfigWrite  = "CONFIG_WRITTEN"
	stepVhostApplied      = "VHOST_APPLIED"
	stepVhostVerified     = "VHOST_VERIFIED"
)

var vhostConfigStepOrder = []string{
	stepVhostValidated,
	stepVhostConfigGen,
	stepVhostConfigWrite,
	stepVhostApplied,
	stepVhostVerified,
}

type vhostJournal struct {
	Version      int             `json:"version"`
	JobID        string          `json:"job_id"`
	Site         string          `json:"site"`
	Domain       string          `json:"domain"`
	Actor        string          `json:"actor"`
	StartedAt    time.Time       `json:"started_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	Completed    map[string]bool `json:"completed"`
	ConfigPath   string          `json:"config_path,omitempty"`

	path string // journal file path (not serialized)
}

// vhostJournalPath is the on-disk location for a job's vhost configuration journal.
func vhostJournalPath(recoveryRoot, jobID string) (string, error) {
	if strings.TrimSpace(recoveryRoot) == "" {
		return "", errors.New("recovery root is empty")
	}
	if !validJobIDForJournal(jobID) {
		return "", fmt.Errorf("invalid job id for vhost journal: %q", jobID)
	}
	return filepath.Join(recoveryRoot, "vhost-config-"+jobID+".json"), nil
}

// loadOrCreateVhostJournal reads an existing journal for jobID or
// creates a new one. A caller that receives a fresh journal has
// zero completed steps recorded.
func loadOrCreateVhostJournal(recoveryRoot, jobID, site, domain, actor string) (*vhostJournal, error) {
	path, err := vhostJournalPath(recoveryRoot, jobID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(recoveryRoot, 0700); err != nil {
		return nil, fmt.Errorf("prepare recovery root: %w", err)
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var journal vhostJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return nil, fmt.Errorf("decode vhost journal %s: %w", filepath.Base(path), err)
		}
		if journal.Version != vhostJournalVersion || journal.JobID != jobID || journal.Site != site {
			return nil, fmt.Errorf("vhost journal for job %s does not match request", jobID)
		}
		if journal.Completed == nil {
			journal.Completed = map[string]bool{}
		}
		journal.path = path
		return &journal, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read vhost journal: %w", err)
	}
	now := time.Now().UTC()
	return &vhostJournal{
		Version:   vhostJournalVersion,
		JobID:     jobID,
		Site:      site,
		Domain:    domain,
		Actor:     actor,
		StartedAt: now,
		UpdatedAt: now,
		Completed: map[string]bool{},
		path:      path,
	}, nil
}

func (j *vhostJournal) isComplete(step string) bool {
	if j == nil {
		return false
	}
	return j.Completed[step]
}

// markComplete records that step has committed and persists the journal
// atomically. Callers MUST have finished the step's work before calling
// this — a partial write followed by a crash would leave the journal
// claiming completion for a step that didn't finish.
func (j *vhostJournal) markComplete(step string) error {
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
func (j *vhostJournal) cleanup() error {
	if j == nil || j.path == "" {
		return nil
	}
	if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove vhost journal: %w", err)
	}
	return nil
}

// setConfigPath records where vhost configuration is stored.
func (j *vhostJournal) setConfigPath(path string) {
	if j == nil {
		return
	}
	j.ConfigPath = path
}
