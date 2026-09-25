package main

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

const releaseActivationJournalVersion = 1

// releaseActivationJournal closes the crash window between publishing a
// release and sealing the site. The filesystem swap is performed by
// SiteManager; this journal tells startup whether an in-flight swap must be
// rolled back before the control plane accepts traffic.
type releaseActivationJournal struct {
	Version    int       `json:"version"`
	ID         string    `json:"id"`
	Site       string    `json:"site"`
	StagedRoot string    `json:"staged_root"`
	Previous   string    `json:"previous,omitempty"`
	State      string    `json:"state"` // prepared, activated
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`

	path string
}

func newReleaseActivationJournal(recoveryRoot, site, stagedRoot string) (*releaseActivationJournal, error) {
	if strings.TrimSpace(recoveryRoot) == "" || safeUser(site) == "" || strings.TrimSpace(stagedRoot) == "" {
		return nil, errors.New("release activation journal requires recovery root, site, and staging path")
	}
	if err := os.MkdirAll(recoveryRoot, 0700); err != nil {
		return nil, fmt.Errorf("prepare release activation journal root: %w", err)
	}
	id := newRequestID()
	path, err := safePath(recoveryRoot, "release-activation-"+id+".json")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &releaseActivationJournal{Version: releaseActivationJournalVersion, ID: id, Site: site, StagedRoot: stagedRoot, State: "prepared", StartedAt: now, UpdatedAt: now, path: path}, nil
}

func (j *releaseActivationJournal) markActivated(previous string) error {
	if j == nil || j.path == "" {
		return errors.New("release activation journal is not initialized")
	}
	j.Previous = previous
	j.State = "activated"
	j.UpdatedAt = time.Now().UTC()
	return j.persist()
}

func (j *releaseActivationJournal) markCommitted() error {
	if j == nil || j.path == "" {
		return errors.New("release activation journal is not initialized")
	}
	j.State = "committed"
	j.UpdatedAt = time.Now().UTC()
	return j.persist()
}

func (j *releaseActivationJournal) persist() error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(j.path, append(data, '\n'), 0600)
}

func (j *releaseActivationJournal) cleanup() error {
	if j == nil || j.path == "" {
		return nil
	}
	if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove release activation journal: %w", err)
	}
	return nil
}

func recoverReleaseActivationJournals(cfg Config) ([]string, error) {
	entries, err := os.ReadDir(cfg.RecoveryRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read release activation journals: %w", err)
	}
	manager, err := newSiteManagerForConfig(cfg)
	if err != nil {
		return nil, err
	}
	recovered := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "release-activation-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(cfg.RecoveryRoot, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return recovered, fmt.Errorf("read release activation journal %s: %w", entry.Name(), err)
		}
		var journal releaseActivationJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return recovered, fmt.Errorf("decode release activation journal %s: %w", entry.Name(), err)
		}
		if journal.Version != releaseActivationJournalVersion || journal.ID == "" || safeUser(journal.Site) == "" || journal.StagedRoot == "" {
			return recovered, fmt.Errorf("release activation journal %s is invalid", entry.Name())
		}
		journal.path = path
		ctx := context.Background()
		switch journal.State {
		case "prepared":
			public, err := safePath(cfg.WebRoot, "sites", journal.Site, "public")
			if err != nil {
				return recovered, fmt.Errorf("resolve prepared release activation %s: %w", journal.ID, err)
			}
			stageExists, err := releaseStageExists(cfg.WebRoot, journal.Site, journal.StagedRoot)
			if err != nil {
				return recovered, fmt.Errorf("inspect prepared release activation %s: %w", journal.ID, err)
			}
			_, publicErr := os.Lstat(public)
			previous, findErr := findPreviousRelease(cfg.WebRoot, journal.Site)
			if findErr != nil {
				return recovered, fmt.Errorf("inspect prepared release activation %s: %w", journal.ID, findErr)
			}
			switch {
			case stageExists && publicErr == nil:
				// The swap never started; discard only this journal's stage.
				if err := manager.DiscardReleaseStaging(ctx, journal.Site, journal.StagedRoot); err != nil {
					return recovered, fmt.Errorf("discard prepared release activation %s: %w", journal.ID, err)
				}
			case !stageExists && previous != "":
				// The stage moved to public (or public was moved away first).
				if err := manager.RollbackStagedActivation(ctx, journal.Site, previous); err != nil {
					return recovered, fmt.Errorf("rollback prepared release activation %s: %w", journal.ID, err)
				}
			default:
				return recovered, fmt.Errorf("prepared release activation %s is in an ambiguous filesystem state", journal.ID)
			}
		case "activated":
			if err := manager.RollbackStagedActivation(ctx, journal.Site, journal.Previous); err != nil {
				return recovered, fmt.Errorf("rollback release activation %s: %w", journal.ID, err)
			}
		case "committed":
			// The mutation completed. A crash only interrupted journal
			// cleanup, so never roll it back on startup.
		default:
			return recovered, fmt.Errorf("release activation journal %s has unknown state %q", journal.ID, journal.State)
		}
		if err := journal.cleanup(); err != nil {
			return recovered, err
		}
		recovered = append(recovered, journal.ID)
	}
	return recovered, nil
}

func releaseStageExists(webRoot, site, stagedRoot string) (bool, error) {
	siteRoot, err := safePath(webRoot, "sites", site)
	if err != nil {
		return false, err
	}
	abs, err := filepath.Abs(stagedRoot)
	if err != nil || filepath.Dir(abs) != siteRoot || !strings.HasPrefix(filepath.Base(abs), ".stepanel-release-") {
		return false, errors.New("invalid release staging path")
	}
	_, err = os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func findPreviousRelease(webRoot, site string) (string, error) {
	if safeUser(site) == "" {
		return "", errors.New("invalid site name")
	}
	siteRoot, err := safePath(webRoot, "sites", site)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(siteRoot)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var previous string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".stepanel-previous-") {
			continue
		}
		candidate, pathErr := safePath(siteRoot, entry.Name())
		if pathErr != nil {
			return "", pathErr
		}
		if previous == "" || entry.Name() > filepath.Base(previous) {
			previous = candidate
		}
	}
	return previous, nil
}
