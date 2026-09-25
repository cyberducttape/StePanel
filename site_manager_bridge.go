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
		var err error
		manager, err = siteauthority.NewDefaultManager(a.Config.WebRoot)
		if err != nil {
			return fmt.Errorf("initialize site manager: %w", err)
		}
	}
	if _, err := manager.ActivateStaged(ctx, name, stagedRoot); err != nil {
		return err
	}
	return nil
}
