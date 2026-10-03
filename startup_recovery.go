package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

// recoverUncleanShutdown repairs the filesystem and database state an
// interrupted process can leave behind: managed databases created by an
// unfinished site transaction, half-applied site transactions, interrupted
// release activations, and release staging nothing will use. It runs once
// during panel startup, before new work is admitted, and is the same code
// the crash-recovery drills exercise after a real SIGKILL. Failures are
// isolated per item and returned rather than stopping recovery.
func recoverUncleanShutdown(cfg Config, siteManager siteauthority.Manager) []error {
	var failures []error
	databaseRecoveries, err := RecoverTransactionDatabases(cfg, cfg.RecoveryRoot)
	if err != nil {
		failures = append(failures, err)
		log.Printf("recover interrupted database transactions (continuing with isolated failures): %v", err)
	}
	for _, id := range databaseRecoveries {
		log.Printf("recovered databases for interrupted site transaction %s", id)
		if err := Audit(cfg.AuditLog, "restore.database-recovered", id, "managed databases removed after unclean shutdown"); err != nil {
			failures = append(failures, fmt.Errorf("audit database recovery %s: %w", id, err))
		}
	}
	recovered, err := RecoverSiteTransactions(cfg.RecoveryRoot, cfg.WebRoot, cfg.MailRoot)
	if err != nil {
		failures = append(failures, err)
		log.Printf("recover interrupted site transactions (continuing with isolated failures): %v", err)
	}
	for _, id := range recovered {
		txn, loadErr := loadSiteTransaction(filepath.Join(cfg.RecoveryRoot, id))
		if loadErr != nil {
			failures = append(failures, fmt.Errorf("load recovered site transaction %s: %w", id, loadErr))
			log.Printf("load recovered site transaction %s: %v", id, loadErr)
			continue
		}
		if txn.HadExisting {
			if sealErr := siteHelper(cfg, "seal", txn.Site); sealErr != nil {
				failures = append(failures, fmt.Errorf("seal recovered site transaction %s: %w", id, sealErr))
				log.Printf("seal recovered site transaction %s: %v", id, sealErr)
				continue
			}
		}
		log.Printf("recovered interrupted site transaction %s", id)
		recoveryMessage := "previous site restored after unclean shutdown"
		if !txn.HadExisting {
			recoveryMessage = "new site removed after unclean shutdown"
		}
		if err := Audit(cfg.AuditLog, "restore.recovered", id, recoveryMessage); err != nil {
			failures = append(failures, fmt.Errorf("audit site recovery %s: %w", id, err))
		}
	}
	releaseRecoveries, err := recoverReleaseActivationJournals(cfg)
	if err != nil {
		failures = append(failures, err)
		log.Printf("recover interrupted release activations (continuing with isolated failures): %v", err)
	}
	for _, id := range releaseRecoveries {
		log.Printf("recovered interrupted release activation %s", id)
		if err := Audit(cfg.AuditLog, "release.activation-recovered", id, "release activation reconciled after unclean shutdown"); err != nil {
			failures = append(failures, fmt.Errorf("audit release recovery %s: %w", id, err))
		}
	}
	// Pipeline checkout/build staging has no activation journal yet. If the
	// process dies in that window, discard only manager-owned release trees
	// after journal recovery has had first opportunity to use them.
	if err == nil {
		if orphaned, cleanupErr := siteManager.CleanupOrphanedStaging(context.Background(), orphanedStagingMinAge); cleanupErr != nil {
			failures = append(failures, fmt.Errorf("cleanup orphaned staging: %w", cleanupErr))
		} else if orphaned > 0 {
			log.Printf("cleaned %d orphaned staging tree(s)", orphaned)
		}
	}
	return failures
}
