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
const maxAppActivationJournalBytes = 8 << 20

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

// recoverAppActivationJournals finishes or undoes interrupted app
// activations at startup. Each journal is recovered under its site's
// mutation lock: in broker mode the root broker refuses a mutating request
// without the fencing token that lock carries in its context, and the lock
// also waits out the lease of an operation the crash interrupted. lockSite
// may be nil (tests and installs without a lock table).
func recoverAppActivationJournals(cfg Config, lockSite siteLocker, lockWait time.Duration) ([]string, error) {
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
		data, err := readStateFile(path, maxAppActivationJournalBytes)
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
		siteCtx, unlock, err := lockAppRecoverySite(lockSite, lockWait, journal.Site)
		if err != nil {
			return recovered, fmt.Errorf("lock application %s for activation recovery: %w", journal.Site, err)
		}
		id, err := recoverAppActivationJournal(siteCtx, cfg, &journal)
		unlock()
		if err != nil {
			return recovered, err
		}
		recovered = append(recovered, id)
	}
	return recovered, nil
}

// lockAppRecoverySite takes site's mutation lock for startup recovery and
// returns its context, which carries the broker fencing token.
func lockAppRecoverySite(lockSite siteLocker, lockWait time.Duration, site string) (context.Context, func(), error) {
	if lockSite == nil {
		return context.Background(), func() {}, nil
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), lockWait)
	siteCtx, release, err := lockSite(waitCtx, site)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return siteCtx, func() { release(); cancel() }, nil
}

// recoverAppActivationJournal rolls one validated journal forward (committed)
// or back (anything earlier) and removes it. ctx holds the site's lock.
func recoverAppActivationJournal(ctx context.Context, cfg Config, journal *appActivationJournal) (string, error) {
	if journal.State == "committed" || journal.State == "manifest_committed" {
		if journal.Version >= 3 {
			if err := reconcileCommittedAppActivation(ctx, cfg, journal); err != nil {
				return "", fmt.Errorf("reconcile committed application %s: %w", journal.Site, err)
			}
		} else if err := validateCommittedAppManifest(cfg, journal.Site); err != nil {
			return "", fmt.Errorf("validate committed application %s: %w", journal.Site, err)
		}
		if err := journal.cleanup(); err != nil {
			return "", err
		}
		return journal.ID, nil
	}
	restoreCtx, cancel := context.WithTimeout(ctx, helperServiceLifecycleTimeout)
	var restoreErr error
	if journal.HadPrevious && journal.RestoreManifest != nil {
		restoreErr = restoreAppProcess(restoreCtx, cfg, *journal.RestoreManifest)
	} else if !journal.HadPrevious {
		restoreErr = runAppLifecycle(restoreCtx, cfg, "stop", journal.Site)
	}
	cancel()
	if restoreErr != nil {
		return "", fmt.Errorf("restore application %s from activation journal: %w", journal.Site, restoreErr)
	}
	if journal.HadPrevious {
		if err := writeAtomic(journal.ManifestPath, journal.Previous, 0600); err != nil {
			return "", fmt.Errorf("restore application %s manifest: %w", journal.Site, err)
		}
	} else if err := removeAppManifest(cfg, journal.Site, journal.ManifestPath); err != nil {
		return "", fmt.Errorf("remove interrupted application %s manifest: %w", journal.Site, err)
	}
	if journal.Version >= 2 {
		if err := restoreAppBackup(cfg, journal.Site, journal.PreviousBackup, journal.HadPreviousBackup); err != nil {
			return "", fmt.Errorf("restore application %s rollback manifest: %w", journal.Site, err)
		}
	}
	if err := journal.cleanup(); err != nil {
		return "", err
	}
	return journal.ID, nil
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

func reconcileCommittedAppActivation(ctx context.Context, cfg Config, journal *appActivationJournal) error {
	if journal == nil || len(journal.TargetManifest) == 0 {
		return errors.New("committed activation target is missing")
	}
	var target AppManifest
	if err := json.Unmarshal(journal.TargetManifest, &target); err != nil || !validAppManifest(cfg, target, journal.Site) {
		return errors.New("committed activation target is invalid")
	}
	restoreCtx, cancel := context.WithTimeout(ctx, helperServiceLifecycleTimeout)
	defer cancel()
	if err := restoreAppProcess(restoreCtx, cfg, target); err != nil {
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
