package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const appActivationJournalVersion = 3

// appActivationJournal makes application runtime and manifest publication
// recoverable across process death. An uncommitted journal always restores
// the prior generation; this is deliberately conservative when the runtime
// state cannot be proven to match the new manifest.
type appActivationJournal struct {
	Version           int          `json:"version"`
	ID                string       `json:"id"`
	Site              string       `json:"site"`
	ManifestPath      string       `json:"manifest_path"`
	Previous          []byte       `json:"previous,omitempty"`
	HadPrevious       bool         `json:"had_previous"`
	PreviousBackup    []byte       `json:"previous_backup,omitempty"`
	HadPreviousBackup bool         `json:"had_previous_backup,omitempty"`
	TargetManifest    []byte       `json:"target_manifest,omitempty"`
	TargetBackup      []byte       `json:"target_backup,omitempty"`
	HadTargetBackup   bool         `json:"had_target_backup,omitempty"`
	RestoreManifest   *AppManifest `json:"restore_manifest,omitempty"`
	State             string       `json:"state"`
	UpdatedAt         time.Time    `json:"updated_at"`
	path              string
}

func newAppActivationJournal(cfg Config, site, manifestPath string, previous []byte, restore *AppManifest) (*appActivationJournal, error) {
	return newAppActivationJournalWithBackup(cfg, site, manifestPath, previous, restore, nil, false)
}

func newAppActivationJournalWithBackup(cfg Config, site, manifestPath string, previous []byte, restore *AppManifest, previousBackup []byte, hadPreviousBackup bool) (*appActivationJournal, error) {
	if strings.TrimSpace(cfg.AppRoot) == "" || safeUser(site) == "" || manifestPath == "" {
		return nil, errors.New("application activation journal requires app root, site, and manifest")
	}
	if err := os.MkdirAll(cfg.AppRoot, 0750); err != nil {
		return nil, fmt.Errorf("prepare application journal root: %w", err)
	}
	id := newRequestID()
	path, err := safePath(cfg.AppRoot, ".activation-"+id+".json")
	if err != nil {
		return nil, err
	}
	return &appActivationJournal{Version: appActivationJournalVersion, ID: id, Site: site, ManifestPath: manifestPath, Previous: previous, HadPrevious: len(previous) > 0, PreviousBackup: previousBackup, HadPreviousBackup: hadPreviousBackup, RestoreManifest: restore, State: "prepared", UpdatedAt: time.Now().UTC(), path: path}, nil
}

func (j *appActivationJournal) setState(state string) error {
	if j == nil || j.path == "" {
		return errors.New("application activation journal is not initialized")
	}
	j.State = state
	j.UpdatedAt = time.Now().UTC()
	return j.persist()
}

func (j *appActivationJournal) setTarget(manifest, backup []byte, hadBackup bool) error {
	if j == nil || j.path == "" || j.State != "prepared" || len(manifest) == 0 {
		return errors.New("application activation target is invalid")
	}
	j.TargetManifest = append([]byte(nil), manifest...)
	j.TargetBackup = append([]byte(nil), backup...)
	j.HadTargetBackup = hadBackup
	j.UpdatedAt = time.Now().UTC()
	return j.persist()
}

func (j *appActivationJournal) persist() error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(j.path, append(data, '\n'), 0600)
}

func (j *appActivationJournal) cleanup() error {
	if j == nil || j.path == "" {
		return nil
	}
	if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func recoverAppActivationJournals(cfg Config) ([]string, error) {
	entries, err := os.ReadDir(cfg.AppRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read application activation journals: %w", err)
	}
	var recovered []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".activation-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(cfg.AppRoot, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return recovered, err
		}
		var journal appActivationJournal
		if err := json.Unmarshal(data, &journal); err != nil || journal.Version < 1 || journal.Version > appActivationJournalVersion || safeUser(journal.Site) == "" || filepath.Clean(journal.ManifestPath) != filepath.Clean(filepath.Join(cfg.AppRoot, journal.Site+".json")) {
			return recovered, fmt.Errorf("invalid application activation journal %s", entry.Name())
		}
		if !validAppActivationState(journal.State) {
			return recovered, fmt.Errorf("application activation journal %s has unknown state %q", entry.Name(), journal.State)
		}
		if journal.HadPrevious {
			var recorded AppManifest
			if journal.RestoreManifest == nil || json.Unmarshal(journal.Previous, &recorded) != nil || !validAppManifest(cfg, recorded, journal.Site) || recorded != *journal.RestoreManifest {
				return recovered, fmt.Errorf("application activation journal %s has invalid previous generation", entry.Name())
			}
		} else if journal.RestoreManifest != nil || len(journal.Previous) != 0 {
			return recovered, fmt.Errorf("application activation journal %s has inconsistent previous generation", entry.Name())
		}
		journal.path = path
		if journal.State == "committed" || journal.State == "manifest_committed" {
			if journal.Version >= 3 {
				if err := reconcileCommittedAppActivation(cfg, &journal); err != nil {
					return recovered, fmt.Errorf("reconcile committed application %s: %w", journal.Site, err)
				}
			} else if err := validateCommittedAppManifest(cfg, journal.Site); err != nil {
				return recovered, fmt.Errorf("validate committed application %s: %w", journal.Site, err)
			}
			if err := journal.cleanup(); err != nil {
				return recovered, err
			}
			recovered = append(recovered, journal.ID)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), helperServiceLifecycleTimeout)
		var restoreErr error
		if journal.HadPrevious && journal.RestoreManifest != nil {
			restoreErr = restoreAppProcess(ctx, cfg, *journal.RestoreManifest)
		} else if !journal.HadPrevious {
			restoreErr = runAppLifecycle(ctx, cfg, "stop", journal.Site)
		}
		cancel()
		if restoreErr != nil {
			return recovered, fmt.Errorf("restore application %s from activation journal: %w", journal.Site, restoreErr)
		}
		if journal.HadPrevious {
			if err := writeAtomic(journal.ManifestPath, journal.Previous, 0600); err != nil {
				return recovered, fmt.Errorf("restore application %s manifest: %w", journal.Site, err)
			}
		} else if err := removeAppManifest(cfg, journal.Site, journal.ManifestPath); err != nil {
			return recovered, fmt.Errorf("remove interrupted application %s manifest: %w", journal.Site, err)
		}
		if journal.Version >= 2 {
			if err := restoreAppBackup(cfg, journal.Site, journal.PreviousBackup, journal.HadPreviousBackup); err != nil {
				return recovered, fmt.Errorf("restore application %s rollback manifest: %w", journal.Site, err)
			}
		}
		if err := journal.cleanup(); err != nil {
			return recovered, err
		}
		recovered = append(recovered, journal.ID)
	}
	return recovered, nil
}

func validateCommittedAppManifest(cfg Config, site string) error {
	data, exists, err := readAppStateFile(cfg, site, ".json")
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("committed manifest is missing")
	}
	var manifest AppManifest
	if err := json.Unmarshal(data, &manifest); err != nil || !validAppManifest(cfg, manifest, site) {
		return errors.New("committed manifest is invalid")
	}
	return nil
}

func reconcileCommittedAppActivation(cfg Config, journal *appActivationJournal) error {
	if journal == nil || len(journal.TargetManifest) == 0 {
		return errors.New("committed activation target is missing")
	}
	var target AppManifest
	if err := json.Unmarshal(journal.TargetManifest, &target); err != nil || !validAppManifest(cfg, target, journal.Site) {
		return errors.New("committed activation target is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), helperServiceLifecycleTimeout)
	defer cancel()
	if err := restoreAppProcess(ctx, cfg, target); err != nil {
		return err
	}
	if err := writeAtomic(journal.ManifestPath, journal.TargetManifest, 0600); err != nil {
		return err
	}
	return restoreAppBackup(cfg, journal.Site, journal.TargetBackup, journal.HadTargetBackup)
}

func validAppActivationState(state string) bool {
	switch state {
	case "prepared", "runtime_applying", "runtime_applied", "manifest_committed", "committed":
		return true
	default:
		return false
	}
}
