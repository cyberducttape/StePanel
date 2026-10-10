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

const appActivationJournalVersion = 1

// appActivationJournal makes application runtime and manifest publication
// recoverable across process death. An uncommitted journal always restores
// the prior generation; this is deliberately conservative when the runtime
// state cannot be proven to match the new manifest.
type appActivationJournal struct {
	Version         int          `json:"version"`
	ID              string       `json:"id"`
	Site            string       `json:"site"`
	ManifestPath    string       `json:"manifest_path"`
	Previous        []byte       `json:"previous,omitempty"`
	HadPrevious     bool         `json:"had_previous"`
	RestoreManifest *AppManifest `json:"restore_manifest,omitempty"`
	State           string       `json:"state"`
	UpdatedAt       time.Time    `json:"updated_at"`
	path            string
}

func newAppActivationJournal(cfg Config, site, manifestPath string, previous []byte, restore *AppManifest) (*appActivationJournal, error) {
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
	return &appActivationJournal{Version: appActivationJournalVersion, ID: id, Site: site, ManifestPath: manifestPath, Previous: previous, HadPrevious: len(previous) > 0, RestoreManifest: restore, State: "prepared", UpdatedAt: time.Now().UTC(), path: path}, nil
}

func (j *appActivationJournal) setState(state string) error {
	if j == nil || j.path == "" {
		return errors.New("application activation journal is not initialized")
	}
	j.State = state
	j.UpdatedAt = time.Now().UTC()
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
		if err := json.Unmarshal(data, &journal); err != nil || journal.Version != appActivationJournalVersion || safeUser(journal.Site) == "" || filepath.Clean(journal.ManifestPath) != filepath.Clean(filepath.Join(cfg.AppRoot, journal.Site+".json")) {
			return recovered, fmt.Errorf("invalid application activation journal %s", entry.Name())
		}
		journal.path = path
		if journal.State == "committed" {
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
		if err := journal.cleanup(); err != nil {
			return recovered, err
		}
		recovered = append(recovered, journal.ID)
	}
	return recovered, nil
}
