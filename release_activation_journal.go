package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
	"github.com/cyberducttape/StePanel/internal/state"
)

const releaseActivationJournalVersion = 1
const maxReleaseActivationJournalBytes = 1 << 20

// releaseActivationJournal closes the crash window between publishing a
// release and sealing the site. The filesystem swap is performed by
// SiteManager; this journal tells startup whether an in-flight swap must be
// rolled back before the control plane accepts traffic.
type releaseActivationJournal struct {
	Version    int    `json:"version"`
	ID         string `json:"id"`
	Site       string `json:"site"`
	StagedRoot string `json:"staged_root"`
	// StageDevice and StageInode identify the stage directory when the
	// journal was written. Activation exchanges the stage with the live
	// tree, so after a crash a stage path holding a different directory
	// holds the previous release. Journals from earlier releases lack them.
	StageDevice uint64 `json:"stage_device,omitempty"`
	StageInode  uint64 `json:"stage_inode,omitempty"`
	// LiveDevice and LiveInode identify the document root the activation
	// replaces, so recovery finds the release it moved aside by identity
	// rather than guessing from retained-release names.
	LiveDevice uint64    `json:"live_device,omitempty"`
	LiveInode  uint64    `json:"live_inode,omitempty"`
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
	journal := &releaseActivationJournal{Version: releaseActivationJournalVersion, ID: id, Site: site, StagedRoot: stagedRoot, State: "prepared", StartedAt: now, UpdatedAt: now, path: path}
	// Without a stage to identify, recovery falls back to the
	// rename-only interpretation.
	if info, err := os.Lstat(stagedRoot); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			journal.StageDevice, journal.StageInode = uint64(stat.Dev), stat.Ino
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect release stage: %w", err)
	}
	return journal, nil
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
	if err := state.RemoveDurable(j.path); err != nil {
		return fmt.Errorf("remove release activation journal: %w", err)
	}
	return nil
}

func recoverReleaseActivationJournals(cfg Config) ([]string, error) {
	return recoverReleaseActivationJournalsFor(cfg, "")
}

// recoverReleaseActivationJournalsFor reconciles interrupted release
// activations, only those of site when it is not empty. A site-scoped pass
// leaves journals it cannot attribute to the panel's startup recovery.
func recoverReleaseActivationJournalsFor(cfg Config, site string) ([]string, error) {
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
		data, err := readStateFile(path, maxReleaseActivationJournalBytes)
		if err != nil {
			return recovered, fmt.Errorf("read release activation journal %s: %w", entry.Name(), err)
		}
		var journal releaseActivationJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			if site != "" {
				continue
			}
			return recovered, fmt.Errorf("decode release activation journal %s: %w", entry.Name(), err)
		}
		if site != "" && journal.Site != site {
			continue
		}
		if journal.Version != releaseActivationJournalVersion || journal.ID == "" || safeUser(journal.Site) == "" || journal.StagedRoot == "" {
			return recovered, fmt.Errorf("release activation journal %s is invalid", entry.Name())
		}
		journal.path = path
		ctx := context.Background()
		switch journal.State {
		case "prepared":
			public, err := siteDocRoot(cfg, journal.Site)
			if err != nil {
				return recovered, fmt.Errorf("resolve prepared release activation %s: %w", journal.ID, err)
			}
			stageExists, err := releaseStageExists(cfg, journal.Site, journal.StagedRoot)
			if err != nil {
				return recovered, fmt.Errorf("inspect prepared release activation %s: %w", journal.ID, err)
			}
			_, publicErr := os.Lstat(public)
			previous, findErr := journal.previousRelease(cfg)
			if findErr != nil {
				return recovered, fmt.Errorf("inspect prepared release activation %s: %w", journal.ID, findErr)
			}
			switch {
			case stageExists && publicErr == nil && journal.stageWasExchanged():
				// The stage was exchanged with the live tree, but the old
				// release was not yet moved aside: it is at the stage path
				// and the uncommitted release is live. Put the old one back.
				if err := manager.RollbackStagedActivation(ctx, journal.Site, journal.StagedRoot); err != nil {
					return recovered, fmt.Errorf("restore previous release after interrupted exchange %s: %w", journal.ID, err)
				}
			case stageExists && publicErr == nil:
				// The swap never started; discard only this journal's stage.
				if err := manager.DiscardReleaseStaging(ctx, journal.Site, journal.StagedRoot); err != nil {
					return recovered, fmt.Errorf("discard prepared release activation %s: %w", journal.ID, err)
				}
			case stageExists && errors.Is(publicErr, os.ErrNotExist) && previous != "":
				// The old release was moved aside but publication of the stage
				// never happened. Restore the previous tree, then discard the
				// still-present stage allocated for this activation.
				if err := manager.RollbackStagedActivation(ctx, journal.Site, previous); err != nil {
					return recovered, fmt.Errorf("restore previous release after interrupted activation %s: %w", journal.ID, err)
				}
				if err := manager.DiscardReleaseStaging(ctx, journal.Site, journal.StagedRoot); err != nil {
					return recovered, fmt.Errorf("discard interrupted release stage %s: %w", journal.ID, err)
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

// releaseStageExists reports whether a journaled release stage still exists.
// The stage must be the site's own: an old in-site release stage of a legacy
// site, or a manager release stage on the document root's filesystem (the
// home staging root for a site placed in a hosting account, ADR-0007).
func releaseStageExists(cfg Config, site, stagedRoot string) (bool, error) {
	location, err := siteLocationFor(cfg, site)
	if err != nil {
		return false, err
	}
	abs, err := filepath.Abs(stagedRoot)
	var managerRoot string
	var managerErr error
	if location.Placed {
		managerRoot = sitelayout.StagingRoot(cfg.HomeRoot)
	} else {
		managerRoot, managerErr = safePath(cfg.WebRoot, "sites", ".stepanel-manager-staging")
	}
	base := filepath.Base(abs)
	legacySiteStage := !location.Placed && filepath.Dir(abs) == location.Home && strings.HasPrefix(base, ".stepanel-release-")
	managerStage := managerErr == nil && filepath.Dir(abs) == managerRoot && siteauthority.IsReleaseStagingName(site, base)
	if err != nil || (!legacySiteStage && !managerStage) {
		return false, errors.New("invalid release staging path")
	}
	_, err = os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func findPreviousRelease(cfg Config, site string) (string, error) {
	if safeUser(site) == "" {
		return "", errors.New("invalid site name")
	}
	location, err := siteLocationFor(cfg, site)
	if err != nil {
		return "", err
	}
	siteRoot := location.Home
	entries, err := os.ReadDir(siteRoot)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var previous string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), location.PreviousPrefix) {
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

// stageWasExchanged reports whether the journal's stage path now holds a
// different directory than the stage it recorded: the previous release,
// left there by an activation interrupted after its exchange.
func (j *releaseActivationJournal) stageWasExchanged() bool {
	if j.StageInode == 0 {
		return false
	}
	info, err := os.Lstat(j.StagedRoot)
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (uint64(stat.Dev) != j.StageDevice || stat.Ino != j.StageInode)
}

// recordLive notes the identity of the document root this activation will
// replace. A site without one has nothing to move aside.
func (j *releaseActivationJournal) recordLive(docRoot string) error {
	info, err := os.Lstat(docRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect document root: %w", err)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		j.LiveDevice, j.LiveInode = uint64(stat.Dev), stat.Ino
	}
	return nil
}

// previousRelease finds the release this activation moved aside. A journal
// that recorded the replaced document root finds it by identity among the
// site's retained releases, and returns "" if it is not among them; an
// older journal falls back to the newest retained release.
func (j *releaseActivationJournal) previousRelease(cfg Config) (string, error) {
	if j.LiveInode == 0 {
		return findPreviousRelease(cfg, j.Site)
	}
	location, err := siteLocationFor(cfg, j.Site)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(location.Home)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), location.PreviousPrefix) {
			continue
		}
		candidate, err := safePath(location.Home, entry.Name())
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(candidate)
		if err != nil {
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && uint64(stat.Dev) == j.LiveDevice && stat.Ino == j.LiveInode {
			return candidate, nil
		}
	}
	return "", nil
}

// releaseStageHasJournal reports whether an activation journal still names
// stage. Such a stage may hold a previous release after an indeterminate
// activation, so only journal recovery may remove it.
func releaseStageHasJournal(recoveryRoot, stage string) bool {
	entries, err := os.ReadDir(recoveryRoot)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "release-activation-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := readStateFile(filepath.Join(recoveryRoot, entry.Name()), maxReleaseActivationJournalBytes)
		if err != nil {
			// An unreadable journal could name this stage.
			return true
		}
		var journal releaseActivationJournal
		if json.Unmarshal(data, &journal) != nil || journal.StagedRoot == stage {
			return true
		}
	}
	return false
}
