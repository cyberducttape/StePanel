package main

import (
	"context"
	"fmt"

	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

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
	manager, err := siteauthority.NewDefaultManager(cfg.WebRoot)
	if err != nil {
		return fmt.Errorf("initialize site manager: %w", err)
	}
	if _, err := manager.ActivateStaged(ctx, name, stagedRoot); err != nil {
		return err
	}
	return nil
}

func createSiteManagerStaging(ctx context.Context, cfg Config, prefix string) (string, error) {
	manager, err := siteauthority.NewDefaultManager(cfg.WebRoot)
	if err != nil {
		return "", fmt.Errorf("initialize site manager: %w", err)
	}
	return manager.CreateStaging(ctx, prefix)
}

func discardSiteManagerStaging(ctx context.Context, cfg Config, stagedRoot string) error {
	manager, err := siteauthority.NewDefaultManager(cfg.WebRoot)
	if err != nil {
		return fmt.Errorf("initialize site manager: %w", err)
	}
	return manager.DiscardStaging(ctx, stagedRoot)
}

func (a *App) discardSiteStaging(ctx context.Context, stagedRoot string) error {
	if a.siteManager != nil {
		return a.siteManager.DiscardStaging(ctx, stagedRoot)
	}
	return discardSiteManagerStaging(ctx, a.Config, stagedRoot)
}

func (a *App) createSiteReleaseStaging(ctx context.Context, name, prefix string) (string, error) {
	manager := a.siteManager
	if manager == nil {
		var err error
		manager, err = siteauthority.NewDefaultManager(a.Config.WebRoot)
		if err != nil {
			return "", fmt.Errorf("initialize site manager: %w", err)
		}
	}
	return manager.CreateReleaseStaging(ctx, name, prefix)
}

func (a *App) discardSiteReleaseStaging(ctx context.Context, name, stagedRoot string) error {
	manager := a.siteManager
	if manager == nil {
		var err error
		manager, err = siteauthority.NewDefaultManager(a.Config.WebRoot)
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
		manager, err = siteauthority.NewDefaultManager(a.Config.WebRoot)
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
		manager, err = siteauthority.NewDefaultManager(a.Config.WebRoot)
		if err != nil {
			return fmt.Errorf("initialize site manager: %w", err)
		}
	}
	return manager.RollbackStagedActivation(ctx, name, previous)
}
