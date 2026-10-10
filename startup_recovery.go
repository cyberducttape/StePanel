package stepanel

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

// recoverUncleanShutdown repairs the filesystem and database state an
// interrupted process can leave behind: managed databases created by an
// unfinished site transaction, half-applied site transactions, interrupted
// release activations, and release staging nothing will use. It runs once
// during panel startup, before new work is admitted, and is the same code
// the crash-recovery drills exercise after a real SIGKILL. Failures are
// isolated per item and returned rather than stopping recovery.
//
// lockSite takes the site's durable mutation lease before recovery mutates
// it, so broker requests carry a current fencing token and cannot race a
// lease another process now holds. A nil lockSite (tests without a control
// plane database) runs unlocked.
func recoverUncleanShutdown(cfg Config, siteManager siteauthority.Manager, lockSite siteLocker) []error {
	return recoverInterruptedState(cfg, siteManager, lockSite, "")
}

// recoverInterruptedState is recoverUncleanShutdown limited to one site when
// site is not empty. A worker runs the site-scoped pass, holding the site's
// lease, when it finds a site operation that a crash interrupted, so the site
// is repaired without waiting for the panel to restart.
func recoverInterruptedState(cfg Config, siteManager siteauthority.Manager, lockSite siteLocker, site string) []error {
	var failures []error
	databaseRecoveries, err := recoverTransactionDatabases(cfg, cfg.RecoveryRoot, lockSite, site)
	if err != nil {
		failures = append(failures, err)
		log.Printf("recover interrupted database transactions (continuing with isolated failures): %v", err)
	}
	for _, id := range databaseRecoveries {
		log.Printf("recovered databases for interrupted site transaction %s", id)
		if err := SecurityAuditRequired(cfg.AuditLog, "system", "restore.database-recovered", id, "managed databases removed after unclean shutdown"); err != nil {
			failures = append(failures, fmt.Errorf("audit database recovery %s: %w", id, err))
		}
	}
	var renameForSite func(string, string, string) error
	if cfg.Production || labDirectRootBrokerEnabled() {
		renameForSite = func(transactionSite, source, destination string) error {
			return runTypedSiteMutation(context.Background(), cfg, rootbroker.SiteRequest{
				Action:              "snapshot-restore",
				Site:                transactionSite,
				SnapshotSource:      source,
				SnapshotDestination: destination,
			})
		}
	}
	recovered, err := recoverSiteTransactionsWithRename(cfg.RecoveryRoot, site, renameForSite, cfg.WebRoot, cfg.MailRoot)
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
		siteCtx, unlock, lockErr := lockRecoveredSite(lockSite, txn.Site)
		if lockErr != nil {
			failures = append(failures, fmt.Errorf("lock recovered site transaction %s: %w", id, lockErr))
			log.Printf("lock recovered site transaction %s: %v", id, lockErr)
			continue
		}
		if txn.HadExisting {
			if sealErr := siteHelperContext(siteCtx, cfg, "seal", txn.Site); sealErr != nil {
				unlock()
				failures = append(failures, fmt.Errorf("seal recovered site transaction %s: %w", id, sealErr))
				log.Printf("seal recovered site transaction %s: %v", id, sealErr)
				continue
			}
		}
		if txn.Kind == siteCreationTransactionKind && !txn.HadExisting {
			// An interrupted site creation also leaves the site account, PHP
			// pool and site root the helper prepared; creation refuses any site
			// whose root already exists, so all of it belongs to the
			// interrupted job. Remove it through the documented privileged
			// deletion path, then finalize through SiteManager.
			if deleteErr := siteHelperContext(siteCtx, cfg, "delete", txn.Site); deleteErr != nil {
				unlock()
				failures = append(failures, fmt.Errorf("remove interrupted site creation %s: %w", id, deleteErr))
				log.Printf("remove interrupted site creation %s: %v", id, deleteErr)
				continue
			}
			if deleteErr := siteManager.Delete(siteCtx, txn.Site); deleteErr != nil {
				unlock()
				failures = append(failures, fmt.Errorf("finalize interrupted site creation %s: %w", id, deleteErr))
				log.Printf("finalize interrupted site creation %s: %v", id, deleteErr)
				continue
			}
		}
		unlock()
		log.Printf("recovered interrupted site transaction %s", id)
		recoveryMessage := "previous site restored after unclean shutdown"
		if !txn.HadExisting {
			recoveryMessage = "new site removed after unclean shutdown"
		}
		if err := SecurityAuditRequired(cfg.AuditLog, "system", "restore.recovered", id, recoveryMessage); err != nil {
			failures = append(failures, fmt.Errorf("audit site recovery %s: %w", id, err))
		}
	}
	releaseRecoveries, err := recoverReleaseActivationJournalsFor(cfg, site)
	if err != nil {
		failures = append(failures, err)
		log.Printf("recover interrupted release activations (continuing with isolated failures): %v", err)
	}
	for _, id := range releaseRecoveries {
		log.Printf("recovered interrupted release activation %s", id)
		if err := SecurityAuditRequired(cfg.AuditLog, "system", "release.activation-recovered", id, "release activation reconciled after unclean shutdown"); err != nil {
			failures = append(failures, fmt.Errorf("audit release recovery %s: %w", id, err))
		}
	}
	// Pipeline checkout/build staging has no activation journal yet. If the
	// process dies in that window, discard only manager-owned release trees
	// after journal recovery has had first opportunity to use them. Only the
	// host-wide pass does this: the age check is not site-scoped.
	if err == nil && site == "" {
		if orphaned, cleanupErr := siteManager.CleanupOrphanedStaging(context.Background(), orphanedStagingMinAge); cleanupErr != nil {
			failures = append(failures, fmt.Errorf("cleanup orphaned staging: %w", cleanupErr))
		} else if orphaned > 0 {
			log.Printf("cleaned %d orphaned staging tree(s)", orphaned)
		}
	}
	return failures
}

// siteLocker acquires a site's durable mutation lease; see
// App.acquireSiteMutationLockContext.
type siteLocker func(context.Context, string) (context.Context, func(), error)

// recoveredSiteLockWait bounds how long startup recovery waits for a lease
// left by the interrupted process, which expires after the lease time.
const recoveredSiteLockWait = 3 * time.Minute

func lockRecoveredSite(lockSite siteLocker, site string) (context.Context, func(), error) {
	if lockSite == nil {
		return context.Background(), func() {}, nil
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), recoveredSiteLockWait)
	siteCtx, unlock, err := lockSite(waitCtx, site)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return siteCtx, func() { unlock(); cancel() }, nil
}

// databaseReconcileLease names the lease the startup database reconcile
// holds: the root broker rejects unfenced database mutations.
const databaseReconcileLease = "database:reconcile"

// reconcileDatabaseOperations asks the database helper to finish or roll
// back operations an unclean shutdown interrupted, under a dedicated lease
// so the broker accepts it. A nil lockSite runs unlocked.
func reconcileDatabaseOperations(cfg Config, lockSite siteLocker) error {
	ctx, unlock, err := lockRecoveredSite(lockSite, databaseReconcileLease)
	if err != nil {
		return fmt.Errorf("lock interrupted database reconcile: %w", err)
	}
	defer unlock()
	output, err := runDatabaseHelperContext(ctx, cfg, 2*time.Minute, "", "reconcile")
	if err != nil {
		return fmt.Errorf("reconcile interrupted database operations: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
