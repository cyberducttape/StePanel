package stepanel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

// A backup puts WordPress into maintenance mode while it archives the site.
// If the process is killed, or its context is cancelled, before it turns
// maintenance mode off again, the site stays down. Each activation StePanel
// makes is therefore recorded durably before it happens and removed only
// after a confirmed deactivation; startup and the periodic maintenance tick
// deactivate any session whose owner is gone.

// wordPressMaintenanceResumeTimeout bounds the deactivation that ends a
// backup's maintenance window. It is independent of the backup's own
// context, which may already be cancelled.
const wordPressMaintenanceResumeTimeout = 2 * time.Minute

// wordPressMaintenanceDir holds one marker per site whose maintenance mode
// StePanel turned on. It lives beside the control-plane database, which the
// panel and the worker can both write, and outside the recovery root, whose
// entries are site transactions.
func wordPressMaintenanceDir(cfg Config) (string, error) {
	if cfg.ControlPlaneDB == "" {
		return "", errors.New("control-plane database path is not configured")
	}
	return filepath.Join(filepath.Dir(cfg.ControlPlaneDB), "wordpress-maintenance"), nil
}

func recordWordPressMaintenance(cfg Config, site string) error {
	dir, err := wordPressMaintenanceDir(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create maintenance record directory: %w", err)
	}
	return writeAtomic(filepath.Join(dir, site), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

func clearWordPressMaintenance(cfg Config, site string) error {
	dir, err := wordPressMaintenanceDir(cfg)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, site)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear maintenance record: %w", err)
	}
	return nil
}

// endWordPressMaintenance turns maintenance mode off with its own bounded
// context. It keeps the caller's values (the site lease's fencing token) but
// not its cancellation, so a cancelled backup still restores the site.
func endWordPressMaintenance(parent context.Context, cfg Config, site string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), wordPressMaintenanceResumeTimeout)
	defer cancel()
	if _, err := runWordPress(ctx, cfg, rootbroker.WordPressRequest{Action: "maintenance-deactivate", Site: site}); err != nil {
		return err
	}
	return clearWordPressMaintenance(cfg, site)
}

// recoverWordPressMaintenance deactivates maintenance mode that StePanel
// turned on and never turned off. It takes each site's lease first, waiting
// at most lockWait: a backup still holding the lease is alive and ends its
// own maintenance window, so a busy site is skipped rather than reported.
// A failed deactivation is returned, because the site is still down.
func recoverWordPressMaintenance(cfg Config, lockSite siteLocker, lockWait time.Duration) []error {
	dir, err := wordPressMaintenanceDir(cfg)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return []error{fmt.Errorf("read WordPress maintenance records: %w", err)}
	}
	var failures []error
	for _, entry := range entries {
		site := entry.Name()
		if !entry.Type().IsRegular() || safeUser(site) != site {
			continue
		}
		ctx := context.Background()
		unlock := func() {}
		if lockSite != nil {
			waitCtx, cancel := context.WithTimeout(context.Background(), lockWait)
			siteCtx, release, lockErr := lockSite(waitCtx, site)
			if lockErr != nil {
				cancel()
				log.Printf("WordPress maintenance for %s is still owned by a running operation: %v", site, lockErr)
				continue
			}
			ctx, unlock = siteCtx, func() { release(); cancel() }
		}
		err := endWordPressMaintenance(ctx, cfg, site)
		unlock()
		if err != nil {
			failures = append(failures, fmt.Errorf("WordPress site %s is still in maintenance mode after an interrupted backup: %w", site, err))
			log.Printf("WordPress site %s is still in maintenance mode after an interrupted backup: %v", site, err)
			continue
		}
		log.Printf("ended WordPress maintenance mode left by an interrupted backup of %s", site)
		if err := SecurityAuditRequired(cfg.AuditLog, "system", "wordpress.maintenance.recovered", site, "maintenance mode ended after an interrupted backup"); err != nil {
			failures = append(failures, fmt.Errorf("audit WordPress maintenance recovery %s: %w", site, err))
		}
	}
	return failures
}
