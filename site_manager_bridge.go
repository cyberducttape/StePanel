package stepanel

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

// sitePlacementDB is the control-plane database that places sites inside
// hosting-account homes (ADR-0007). Startup registers it once the database
// is open; until then, and in unit tests that never register one, managers
// see every site in the legacy layout.
var sitePlacementDB atomic.Pointer[sql.DB]

func registerSitePlacementDB(db *sql.DB) { sitePlacementDB.Store(db) }

// newSiteManagerForConfig is the one constructor for site managers. With a
// registered control plane and a home root, placed sites resolve to their
// account home; every other site keeps the legacy web-root layout.
func newSiteManagerForConfig(cfg Config) (*siteauthority.DefaultManager, error) {
	webRoot, err := filepath.Abs(cfg.WebRoot)
	if err != nil {
		return nil, fmt.Errorf("initialize site manager: resolve web root: %w", err)
	}
	db := sitePlacementDB.Load()
	if db == nil || strings.TrimSpace(cfg.HomeRoot) == "" {
		manager, err := siteauthority.NewDefaultManager(webRoot)
		if err != nil {
			return nil, fmt.Errorf("initialize site manager: %w", err)
		}
		return manager, nil
	}
	homeRoot, err := filepath.Abs(cfg.HomeRoot)
	if err != nil {
		return nil, fmt.Errorf("initialize site manager: resolve home root: %w", err)
	}
	manager, err := siteauthority.NewAccountManager(webRoot, homeRoot, func(site string) (sitelayout.Placement, error) {
		return sitelayout.Get(db, site)
	})
	if err != nil {
		return nil, fmt.Errorf("initialize site manager: %w", err)
	}
	return manager, nil
}

// activateStagedSite keeps release publication behind the lifecycle manager
// even for older App constructions used by tests and migration tooling.
func (a *App) activateStagedSite(ctx context.Context, name, stagedRoot string) error {
	manager := a.siteManager
	if manager == nil {
		return activateStagedSiteWithConfig(ctx, a.Config, name, stagedRoot)
	}
	if _, err := manager.ActivateStaged(ctx, name, stagedRoot); err != nil {
		return err
	}
	return nil
}

func activateStagedSiteWithConfig(ctx context.Context, cfg Config, name, stagedRoot string) error {
	manager, err := newSiteManagerForConfig(cfg)
	if err != nil {
		return fmt.Errorf("initialize site manager: %w", err)
	}
	if _, err := manager.ActivateStaged(ctx, name, stagedRoot); err != nil {
		return err
	}
	return nil
}

func createSiteManagerStaging(ctx context.Context, cfg Config, prefix string) (string, *os.File, error) {
	manager, err := newSiteManagerForConfig(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("initialize site manager: %w", err)
	}
	return manager.CreateStagingLease(ctx, prefix)
}

func releaseSiteManagerStagingLease(cfg Config, stagedRoot string, owner *os.File) error {
	manager, err := newSiteManagerForConfig(cfg)
	if err != nil {
		_ = owner.Close()
		return fmt.Errorf("initialize site manager to release staging lease: %w", err)
	}
	return manager.ReleaseStagingLease(stagedRoot, owner)
}

func discardSiteManagerStaging(ctx context.Context, cfg Config, stagedRoot string) error {
	manager, err := newSiteManagerForConfig(cfg)
	if err != nil {
		return fmt.Errorf("initialize site manager: %w", err)
	}
	return manager.DiscardStaging(ctx, stagedRoot)
}

// logStagingDiscardFailure records a staging tree that cleanup could not
// remove. CleanupOrphanedStaging removes it later; the log keeps the leak
// visible. Discards use context.Background deliberately: they must still run
// after the operation's own context was cancelled, and they are local
// filesystem removals that check the context only on entry.
func logStagingDiscardFailure(stagedRoot string, err error) {
	if err != nil {
		log.Printf("discard staging %s (orphaned-staging cleanup will retry): %v", filepath.Base(stagedRoot), err)
	}
}

func (a *App) discardSiteStaging(ctx context.Context, stagedRoot string) error {
	if a.siteManager != nil {
		return a.siteManager.DiscardStaging(ctx, stagedRoot)
	}
	return discardSiteManagerStaging(ctx, a.Config, stagedRoot)
}

func (a *App) createSiteReleaseStaging(ctx context.Context, name, prefix string) (string, *os.File, error) {
	manager := a.siteManager
	if manager == nil {
		var err error
		manager, err = newSiteManagerForConfig(a.Config)
		if err != nil {
			return "", nil, fmt.Errorf("initialize site manager: %w", err)
		}
	}
	return manager.CreateReleaseStagingLease(ctx, name, prefix)
}

func (a *App) discardSiteReleaseStaging(ctx context.Context, name, stagedRoot string) error {
	manager := a.siteManager
	if manager == nil {
		var err error
		manager, err = newSiteManagerForConfig(a.Config)
		if err != nil {
			return fmt.Errorf("initialize site manager: %w", err)
		}
	}
	return manager.DiscardReleaseStaging(ctx, name, stagedRoot)
}

func (a *App) activateReplacingSite(ctx context.Context, name, stagedRoot string) (string, error) {
	manager := a.siteManager
	if manager == nil {
		var err error
		manager, err = newSiteManagerForConfig(a.Config)
		if err != nil {
			return "", fmt.Errorf("initialize site manager: %w", err)
		}
	}
	return manager.ActivateStagedReplacing(ctx, name, stagedRoot)
}

func (a *App) rollbackReplacingSite(ctx context.Context, name, previous string) error {
	manager := a.siteManager
	if manager == nil {
		var err error
		manager, err = newSiteManagerForConfig(a.Config)
		if err != nil {
			return fmt.Errorf("initialize site manager: %w", err)
		}
	}
	return manager.RollbackStagedActivation(ctx, name, previous)
}

// deleteSiteTree removes a canonical site directory through the site manager.
func (a *App) deleteSiteTree(ctx context.Context, name string) error {
	manager := a.siteManager
	if manager == nil {
		var err error
		manager, err = newSiteManagerForConfig(a.Config)
		if err != nil {
			return fmt.Errorf("initialize site manager: %w", err)
		}
	}
	return manager.Delete(ctx, name)
}
