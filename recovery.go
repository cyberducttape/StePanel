package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

type SiteTransaction struct {
	Version      int    `json:"version"`
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Site         string `json:"site"`
	Home         string `json:"home"`
	Backup       string `json:"backup"`
	FailedSite   string `json:"failed_site,omitempty"`
	MailRoot     string `json:"mail_root,omitempty"`
	MailBackup   string `json:"mail_backup,omitempty"`
	MailExisting bool   `json:"mail_existing,omitempty"`
	HadExisting  bool   `json:"had_existing"`
	State        string `json:"state"`
	// RecoveryRequired keeps a journal durable when compensation could not be
	// verified, even if its filesystem transaction reached a terminal state.
	RecoveryRequired string            `json:"recovery_required,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	Databases        []ManagedDatabase `json:"databases,omitempty"`
	// Routes are web server routes the transaction publishes. Recovery and
	// rollback remove them before the site they point at.
	Routes []TrackedRoute `json:"routes,omitempty"`

	dir    string
	rename func(string, string) error
}

// TrackedRoute is a site route recorded before it is applied. Applied is set
// once the web server accepted it: cleanup must remove an applied route, and
// removes a pending one on a best-effort basis because a crash may have
// interrupted it either before or after the web server published it.
type TrackedRoute struct {
	Name    string `json:"name"`
	Applied bool   `json:"applied,omitempty"`
}

func validTrackedRouteName(site, name string) bool {
	prefix := "site-" + site + "-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	var stem string
	switch {
	case strings.HasSuffix(name, ".conf"):
		stem = strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".conf")
	case strings.HasSuffix(name, ".caddy"):
		stem = strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".caddy")
	default:
		return false
	}
	return stem != "" && strings.Trim(stem, "abcdefghijklmnopqrstuvwxyz0123456789_-") == ""
}

// TrackRoute records name as a pending route before it is applied.
func (t *SiteTransaction) TrackRoute(name string) error {
	if !validTrackedRouteName(t.Site, name) {
		return errors.New("invalid tracked route name")
	}
	for _, existing := range t.Routes {
		if existing.Name == name {
			return nil
		}
	}
	t.Routes = append(t.Routes, TrackedRoute{Name: name})
	if err := t.persist(); err != nil {
		t.Routes = t.Routes[:len(t.Routes)-1]
		return err
	}
	return nil
}

// MarkRouteApplied records that the web server published a tracked route.
func (t *SiteTransaction) MarkRouteApplied(name string) error {
	for i := range t.Routes {
		if t.Routes[i].Name == name {
			t.Routes[i].Applied = true
			return t.persist()
		}
	}
	return errors.New("route is not tracked by this transaction")
}

// cleanupRoutes removes the transaction's routes. ctx must carry the site's
// lease: the root broker rejects unfenced vhost mutations.
func (t *SiteTransaction) cleanupRoutes(ctx context.Context, cfg Config) error {
	for len(t.Routes) > 0 {
		route := t.Routes[0]
		if !route.Applied {
			path, pathErr := safePath(cfg.VHostRoot, route.Name)
			if pathErr != nil {
				return fmt.Errorf("inspect pending route %s: %w", route.Name, pathErr)
			}
			if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
				t.Routes = t.Routes[1:]
				if err := t.persist(); err != nil {
					return fmt.Errorf("record absent pending route cleanup: %w", err)
				}
				continue
			} else if statErr != nil {
				return fmt.Errorf("inspect pending route %s: %w", route.Name, statErr)
			}
		}
		if err := runVhostMutation(ctx, cfg, "delete", route.Name); err != nil {
			return fmt.Errorf("remove route %s: %w", route.Name, err)
		}
		t.Routes = t.Routes[1:]
		if err := t.persist(); err != nil {
			return fmt.Errorf("record route cleanup: %w", err)
		}
	}
	return nil
}

type ManagedDatabase struct {
	Name string `json:"name"`
	User string `json:"user,omitempty"`
	Kind string `json:"kind"`
}

func BeginSiteTransaction(root, home, kind string, access SiteCapability) (*SiteTransaction, error) {
	return beginSiteTransaction(root, home, kind, access, os.Rename)
}

// BeginSiteTransactionWithSnapshot is used when the live tree crosses a host
// security boundary that the worker must not manipulate directly. The
// callback receives the validated live path and journal backup path after the
// transaction directory has been persisted.
func BeginSiteTransactionWithSnapshot(root, home, kind string, access SiteCapability, snapshot func(string, string) error) (*SiteTransaction, error) {
	if snapshot == nil {
		return nil, errors.New("site snapshot callback is required")
	}
	return beginSiteTransaction(root, home, kind, access, snapshot)
}

// beginManagedSiteTransaction uses the root broker for renames that cross
// the tenant-owned live-site boundary. The same callback is retained by the
// transaction so an in-process rollback uses the privileged path too; after
// process death, startup recovery supplies its freshly fenced broker callback.
func beginManagedSiteTransaction(ctx context.Context, cfg Config, home, kind string, access SiteCapability) (*SiteTransaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := os.Rename
	if cfg.Production || labDirectRootBrokerEnabled() {
		public, err := filepath.Abs(home)
		if err != nil {
			return nil, fmt.Errorf("resolve managed site snapshot path: %w", err)
		}
		snapshot = func(source, destination string) error {
			action := "snapshot"
			if filepath.Clean(destination) == filepath.Clean(public) {
				action = "snapshot-restore"
			}
			return runTypedSiteMutation(context.WithoutCancel(ctx), cfg, rootbroker.SiteRequest{
				Action:              action,
				Site:                access.Site(),
				SnapshotSource:      source,
				SnapshotDestination: destination,
			})
		}
	}
	return BeginSiteTransactionWithSnapshot(cfg.RecoveryRoot, home, kind, access, snapshot)
}

func beginSiteTransaction(root, home, kind string, access SiteCapability, snapshot func(string, string) error) (*SiteTransaction, error) {
	site := access.Site()
	if safeUser(site) == "" {
		return nil, errors.New("invalid recovery site")
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("site recovery root is not configured")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	home, err = validatedRecoveryPath(home)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create recovery root: %w", err)
	}
	dir, err := os.MkdirTemp(root, time.Now().UTC().Format("20060102-150405-")+site+"-")
	if err != nil {
		return nil, fmt.Errorf("create recovery transaction: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	now := time.Now().UTC()
	txn := &SiteTransaction{
		Version:   1,
		ID:        filepath.Base(dir),
		Kind:      kind,
		Site:      site,
		Home:      home,
		Backup:    filepath.Join(dir, "site-before"),
		State:     "prepared",
		CreatedAt: now,
		UpdatedAt: now,
		dir:       dir,
		rename:    snapshot,
	}
	if existing, statErr := os.Lstat(home); statErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			_ = os.RemoveAll(dir)
			return nil, errors.New("destination site is a symlink")
		}
		txn.HadExisting = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("inspect destination site: %w", statErr)
	}
	if err := txn.persist(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if txn.HadExisting {
		if err := snapshot(home, txn.Backup); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("snapshot existing site: %w", err)
		}
		txn.State = "site-backed-up"
		if err := txn.persist(); err != nil {
			if rollbackErr := os.Rename(txn.Backup, home); rollbackErr != nil {
				return nil, fmt.Errorf("persist recovery transaction: %w (restore backup: %v)", err, rollbackErr)
			}
			_ = os.RemoveAll(dir)
			return nil, err
		}
	}
	if err := failureInjection(transactionFailureOperation(kind), "init"); err != nil {
		if rollbackErr := txn.Rollback(); rollbackErr != nil {
			return nil, fmt.Errorf("failure injection rollback: %w (injected: %v)", rollbackErr, err)
		}
		return nil, err
	}
	return txn, nil
}

func (t *SiteTransaction) Commit() error {
	if err := failureInjection(transactionFailureOperation(t.Kind), "commit"); err != nil {
		return err
	}
	t.State = "committed"
	return t.persist()
}

func (t *SiteTransaction) TrackDatabase(database ManagedDatabase) error {
	if err := validateManagedDatabase(database); err != nil {
		return err
	}
	for _, existing := range t.Databases {
		if existing == database {
			return nil
		}
	}
	t.Databases = append(t.Databases, database)
	if err := t.persist(); err != nil {
		t.Databases = t.Databases[:len(t.Databases)-1]
		return err
	}
	return nil
}

// MarkRecoveryRequired durably records that compensation did not converge.
// Keep the transaction journal until an operator has reconciled the host.
func (t *SiteTransaction) MarkRecoveryRequired(cause error) error {
	if cause == nil {
		return errors.New("recovery intervention requires a cause")
	}
	detail := strings.TrimSpace(cause.Error())
	if detail == "" {
		return errors.New("recovery intervention cause is empty")
	}
	if len(detail) > 4096 {
		detail = detail[:4096]
	}
	previous := t.RecoveryRequired
	t.RecoveryRequired = detail
	if err := t.persist(); err != nil {
		t.RecoveryRequired = previous
		return fmt.Errorf("persist recovery intervention: %w", err)
	}
	return nil
}

// cleanupDatabases drops the transaction's managed databases. ctx must carry
// the site's lease: the root broker rejects unfenced database mutations.
func (t *SiteTransaction) cleanupDatabases(ctx context.Context, cfg Config) error {
	for len(t.Databases) > 0 {
		database := t.Databases[0]
		var err error
		switch database.Kind {
		case "cpmove":
			err = dropDatabase(ctx, cfg, database.Name)
		case "wordpress":
			err = cleanupWPressDatabaseContext(ctx, cfg, database.Name, database.User)
		default:
			err = fmt.Errorf("unsupported managed database kind %q", database.Kind)
		}
		if err != nil {
			return fmt.Errorf("clean up managed database %s: %w", database.Name, err)
		}
		t.Databases = t.Databases[1:]
		if err := t.persist(); err != nil {
			return fmt.Errorf("record managed database cleanup: %w", err)
		}
	}
	return nil
}

func validateManagedDatabase(database ManagedDatabase) error {
	if !validManagedDatabaseIdentifier(database.Name, 64) {
		return errors.New("invalid managed database name")
	}
	switch database.Kind {
	case "cpmove":
		if database.User != "" {
			return errors.New("cpmove database must not specify a user")
		}
	case "wordpress":
		if !validManagedDatabaseIdentifier(database.User, 32) {
			return errors.New("invalid managed WordPress database user")
		}
	default:
		return errors.New("invalid managed database kind")
	}
	return nil
}

func validManagedDatabaseIdentifier(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func (t *SiteTransaction) Rollback() error {
	home, err := validatedRecoveryPath(t.Home)
	if err != nil {
		return fmt.Errorf("invalid recovery site path: %w", err)
	}
	if t.State == "committed" || t.State == "rolled-back" {
		return nil
	}
	homeParent, err := os.OpenRoot(filepath.Dir(home))
	if errors.Is(err, os.ErrNotExist) && !t.HadExisting && t.MailRoot == "" {
		// The new site was already removed (a creation or recovery proof
		// was torn down through stepanel-sitectl delete). There is nothing
		// to preserve and no previous site to restore.
		t.State = "rolled-back"
		return t.persist()
	}
	if err != nil {
		return fmt.Errorf("open recovery site parent: %w", err)
	}
	defer homeParent.Close()
	homeName := filepath.Base(home)
	backup := filepath.Join(t.dir, "site-before")
	mailBackup := filepath.Join(t.dir, "mail-before")
	mailRoot := t.MailRoot
	if mailRoot != "" {
		mailRoot, err = validatedRecoveryPath(mailRoot)
		if err != nil {
			return fmt.Errorf("invalid recovery mail path: %w", err)
		}
	}
	rename := t.rename
	if rename == nil {
		rename = os.Rename
	}
	if t.State == "committed" || t.State == "rolled-back" {
		return nil
	}
	if t.State == "rolling-back" && t.HadExisting {
		_, backupErr := os.Lstat(backup)
		_, homeErr := homeParent.Lstat(homeName)
		if errors.Is(backupErr, os.ErrNotExist) && homeErr == nil {
			t.State = "rolled-back"
			return t.persist()
		}
	}
	t.State = "rolling-back"
	if err := t.persist(); err != nil {
		return fmt.Errorf("record rollback start: %w", err)
	}
	if _, err := homeParent.Lstat(homeName); err == nil {
		failed := filepath.Join(t.dir, "failed-site")
		if _, existsErr := os.Lstat(failed); existsErr == nil {
			failed = filepath.Join(t.dir, "failed-site-"+time.Now().UTC().Format("150405.000000000"))
		}
		if err := rename(home, failed); err != nil {
			return fmt.Errorf("preserve failed site: %w", err)
		}
		t.FailedSite = failed
		if err := t.persist(); err != nil {
			return fmt.Errorf("record failed site: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect failed site: %w", err)
	}
	if t.HadExisting {
		if _, err := os.Lstat(backup); err != nil {
			return fmt.Errorf("recovery backup is unavailable: %w", err)
		}
		if err := rename(backup, home); err != nil {
			return fmt.Errorf("restore previous site: %w", err)
		}
	}
	if mailRoot != "" {
		if _, err := os.Lstat(mailRoot); err == nil {
			failedMail := filepath.Join(t.dir, "failed-mail")
			if renameErr := os.Rename(mailRoot, failedMail); renameErr != nil {
				return fmt.Errorf("preserve failed mail tree: %w", renameErr)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect failed mail tree: %w", err)
		}
		if t.MailExisting {
			if _, err := os.Lstat(mailBackup); err != nil {
				return fmt.Errorf("mail recovery backup is unavailable: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(mailRoot), 0750); err != nil {
				return err
			}
			if err := os.Rename(mailBackup, mailRoot); err != nil {
				return fmt.Errorf("restore previous mail tree: %w", err)
			}
		}
	}
	t.State = "rolled-back"
	return t.persist()
}

func (t *SiteTransaction) persist() error {
	t.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(t.dir, "transaction.json")
	tmp, err := os.CreateTemp(t.dir, ".transaction-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	// Set restrictive permissions immediately to protect transaction state
	if err = tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure transaction file permissions: %w", err)
	}
	// Write transaction data with proper permissions guaranteed
	payload := append(data, '\n')
	if written, writeErr := tmp.Write(payload); writeErr != nil {
		tmp.Close()
		return fmt.Errorf("write transaction state: %w", writeErr)
	} else if written != len(payload) {
		tmp.Close()
		return fmt.Errorf("write transaction state: %w", io.ErrShortWrite)
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync transaction state: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close transaction state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	dir, err := os.Open(t.dir)
	if err != nil {
		return fmt.Errorf("open transaction directory for sync: %w", err)
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return fmt.Errorf("sync transaction directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close transaction directory: %w", closeErr)
	}
	return nil
}

func RecoverSiteTransactions(root string, configuredRoots ...string) ([]string, error) {
	return recoverSiteTransactions(root, "", configuredRoots...)
}

// recoverSiteTransactions rolls back interrupted transactions, only those of
// site when it is not empty. A site-scoped pass leaves entries it cannot
// attribute (unreadable journals) to the panel's startup recovery.
func recoverSiteTransactions(root, site string, configuredRoots ...string) ([]string, error) {
	return recoverSiteTransactionsWithRename(root, site, nil, nil, configuredRoots...)
}

// recoverSiteTransactionsWithRename installs the privileged rename boundary
// used by production startup recovery. Tests and local development retain a
// direct rename callback, while native production recovery supplies the root
// broker callback from startup_recovery.go.
func recoverSiteTransactionsWithRename(root, site string, renameForSite func(context.Context, string, string, string) error, lockSite siteLocker, configuredRoots ...string) ([]string, error) {
	webRoot, mailRoot := recoveryConfiguredRoots(configuredRoots)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	recovered := []string{}
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "quarantine" {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		txn, err := loadSiteTransaction(dir)
		if err != nil && site != "" {
			continue
		}
		if err != nil {
			if quarantineErr := quarantineRecoveryTransaction(root, dir, err); quarantineErr != nil {
				failures = append(failures, fmt.Errorf("quarantine invalid transaction %s: %w (original: %v)", entry.Name(), quarantineErr, err))
			} else {
				failures = append(failures, fmt.Errorf("quarantined invalid transaction %s: %w", entry.Name(), err))
			}
			continue
		}
		if err := validateRecoveredSitePaths(txn, webRoot, mailRoot); err != nil {
			if quarantineErr := quarantineRecoveryTransaction(root, dir, err); quarantineErr != nil {
				failures = append(failures, fmt.Errorf("quarantine unsafe transaction %s: %w (original: %v)", entry.Name(), quarantineErr, err))
			} else {
				failures = append(failures, fmt.Errorf("quarantined unsafe transaction %s: %w", entry.Name(), err))
			}
			continue
		}
		if site != "" && txn.Site != site {
			continue
		}
		if txn.RecoveryRequired != "" {
			failures = append(failures, fmt.Errorf("transaction %s requires operator intervention: %s", txn.ID, txn.RecoveryRequired))
			continue
		}
		if txn.State == "committed" || txn.State == "rolled-back" {
			continue
		}
		if len(txn.Databases) > 0 {
			// Database cleanup must complete before the site is restored. Leaving
			// the transaction pending is safer than rolling back the filesystem
			// while managed databases from the interrupted restore remain active.
			failures = append(failures, fmt.Errorf("recover transaction %s: database cleanup is incomplete", txn.ID))
			continue
		}
		if len(txn.Routes) > 0 {
			// A published route can still direct requests to this site. Never
			// swap or remove its filesystem until the route is confirmed absent.
			// The database-recovery pass owns route cleanup and records progress
			// in the same journal before this rollback pass runs.
			failures = append(failures, fmt.Errorf("recover transaction %s: route cleanup is incomplete", txn.ID))
			continue
		}
		recoveryCtx, unlock, lockErr := lockRecoveredSite(lockSite, txn.Site)
		if lockErr != nil {
			failures = append(failures, fmt.Errorf("recover transaction %s: lock site: %w", txn.ID, lockErr))
			continue
		}
		if renameForSite != nil {
			txn.rename = func(source, destination string) error {
				return renameForSite(recoveryCtx, txn.Site, source, destination)
			}
		}
		rollbackErr := txn.Rollback()
		unlock()
		if rollbackErr != nil {
			failures = append(failures, fmt.Errorf("recover transaction %s: %w", txn.ID, rollbackErr))
			continue
		}
		recovered = append(recovered, txn.ID)
	}
	sort.Strings(recovered)
	return recovered, errors.Join(failures...)
}

// RecoverTransactionDatabases drops databases left by interrupted site
// transactions. lockSite takes each site's lease first so the broker accepts
// the drops; nil runs unlocked (no control-plane database).
func RecoverTransactionDatabases(cfg Config, root string, lockSite siteLocker) ([]string, error) {
	return recoverTransactionDatabases(cfg, root, lockSite, "")
}

// recoverTransactionDatabases is RecoverTransactionDatabases limited to site
// when it is not empty.
func recoverTransactionDatabases(cfg Config, root string, lockSite siteLocker, site string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	recovered := []string{}
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "quarantine" {
			continue
		}
		txn, err := loadSiteTransaction(filepath.Join(root, entry.Name()))
		if err != nil && site != "" {
			continue
		}
		if err != nil {
			if quarantineErr := quarantineRecoveryTransaction(root, filepath.Join(root, entry.Name()), err); quarantineErr != nil {
				failures = append(failures, fmt.Errorf("quarantine invalid transaction %s: %w (original: %v)", entry.Name(), quarantineErr, err))
			} else {
				failures = append(failures, fmt.Errorf("quarantined invalid transaction %s: %w", entry.Name(), err))
			}
			continue
		}
		if err := validateRecoveredSitePaths(txn, cfg.WebRoot, cfg.MailRoot); err != nil {
			if quarantineErr := quarantineRecoveryTransaction(root, filepath.Join(root, entry.Name()), err); quarantineErr != nil {
				failures = append(failures, fmt.Errorf("quarantine unsafe transaction %s: %w (original: %v)", entry.Name(), quarantineErr, err))
			} else {
				failures = append(failures, fmt.Errorf("quarantined unsafe transaction %s: %w", entry.Name(), err))
			}
			continue
		}
		if txn.RecoveryRequired != "" || txn.State == "committed" || txn.State == "rolled-back" || len(txn.Databases) == 0 && len(txn.Routes) == 0 || (site != "" && txn.Site != site) {
			continue
		}
		siteCtx, unlock, lockErr := lockRecoveredSite(lockSite, txn.Site)
		if lockErr != nil {
			failures = append(failures, fmt.Errorf("lock transaction %s site: %w", txn.ID, lockErr))
			continue
		}
		// Routes point at the site and its databases; remove them first.
		err = txn.cleanupRoutes(siteCtx, cfg)
		if err == nil {
			err = txn.cleanupDatabases(siteCtx, cfg)
		}
		unlock()
		if err != nil {
			failures = append(failures, fmt.Errorf("recover transaction %s databases: %w", txn.ID, err))
			continue
		}
		recovered = append(recovered, txn.ID)
	}
	sort.Strings(recovered)
	return recovered, errors.Join(failures...)
}

// recoveryCorruptionObserver is told about every quarantined recovery
// journal; the application reports it as a corruption-category state error.
var recoveryCorruptionObserver func(dir string, cause error)

// quarantineRecoveryTransaction removes malformed journal entries from the
// active recovery scan while preserving them for operator inspection.
func quarantineRecoveryTransaction(root, dir string, cause error) error {
	if recoveryCorruptionObserver != nil {
		recoveryCorruptionObserver(dir, cause)
	}
	quarantine := filepath.Join(root, "quarantine")
	if err := os.MkdirAll(quarantine, 0700); err != nil {
		return err
	}
	name := filepath.Base(dir) + "-" + time.Now().UTC().Format("20060102-150405.000000000")
	target := filepath.Join(quarantine, name)
	if err := os.Rename(dir, target); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, "quarantine-reason.txt"), []byte(cause.Error()+"\n"), 0600)
}

func loadSiteTransaction(dir string) (*SiteTransaction, error) {
	data, err := os.ReadFile(filepath.Join(dir, "transaction.json"))
	if err != nil {
		return nil, fmt.Errorf("read recovery transaction %s: %w", filepath.Base(dir), err)
	}
	var txn SiteTransaction
	if err := json.Unmarshal(data, &txn); err != nil {
		return nil, fmt.Errorf("decode recovery transaction %s: %w", filepath.Base(dir), err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if txn.Version != 1 || txn.ID != filepath.Base(dir) || safeUser(txn.Site) == "" {
		return nil, fmt.Errorf("invalid recovery transaction %s", filepath.Base(dir))
	}
	if txn.Home, err = validatedRecoveryPath(txn.Home); err != nil {
		return nil, fmt.Errorf("invalid recovery transaction %s: unsafe site path", filepath.Base(dir))
	}
	for _, database := range txn.Databases {
		if err := validateManagedDatabase(database); err != nil {
			return nil, fmt.Errorf("recovery transaction %s contains an invalid managed database: %w", txn.ID, err)
		}
	}
	for _, route := range txn.Routes {
		if !validTrackedRouteName(txn.Site, route.Name) {
			return nil, fmt.Errorf("recovery transaction %s contains an invalid route", txn.ID)
		}
	}
	expectedBackup := filepath.Join(dir, "site-before")
	if filepath.Clean(txn.Backup) != expectedBackup || txn.FailedSite != "" && !strings.HasPrefix(filepath.Clean(txn.FailedSite), dir+string(os.PathSeparator)) {
		return nil, fmt.Errorf("recovery transaction %s contains unsafe paths", txn.ID)
	}
	if txn.MailRoot != "" && (filepath.IsAbs(txn.MailRoot) == false || filepath.Clean(txn.MailBackup) != filepath.Join(dir, "mail-before")) {
		return nil, fmt.Errorf("recovery transaction %s contains unsafe mail paths", txn.ID)
	}
	txn.dir = dir
	return &txn, nil
}

func loadRecoveryIntervention(root, id string) (*SiteTransaction, error) {
	if id == "" || filepath.Base(id) != id || strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
		return nil, errors.New("invalid recovery transaction ID")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve recovery root: %w", err)
	}
	dir := filepath.Join(root, id)
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect recovery transaction: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("recovery transaction is not a real directory")
	}
	txn, err := loadSiteTransaction(dir)
	if err != nil {
		return nil, err
	}
	if txn.RecoveryRequired == "" {
		return nil, errors.New("recovery transaction has no pending operator intervention")
	}
	return txn, nil
}

// resolveRecoveryIntervention clears a journal's operator marker only after an
// explicit out-of-band host reconciliation. The CLI caller holds both panel
// and worker process locks and records the resolution attempt in the audit log.
func resolveRecoveryIntervention(root, id string) error {
	txn, err := loadRecoveryIntervention(root, id)
	if err != nil {
		return err
	}
	txn.RecoveryRequired = ""
	if err := txn.persist(); err != nil {
		return fmt.Errorf("clear recovery intervention: %w", err)
	}
	return nil
}

// validatedRecoveryPath accepts only an absolute, canonical path. Recovery
// journals are persisted state and must not be allowed to smuggle traversal
// components into filesystem operations. Production recovery additionally
// binds this path to Config.WebRoot in validateRecoveredSitePaths.
func validatedRecoveryPath(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return "", errors.New("recovery path must be absolute")
	}
	clean := filepath.Clean(path)
	if clean != path || strings.Contains(path, ".."+string(os.PathSeparator)) || strings.HasSuffix(path, string(os.PathSeparator)+"..") {
		return "", errors.New("recovery path is not canonical")
	}
	return clean, nil
}

func CleanupSiteTransactions(root string, maxAge time.Duration, configuredRoots ...string) error {
	webRoot, mailRoot := recoveryConfiguredRoots(configuredRoots)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "quarantine" {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		txn, err := loadSiteTransaction(dir)
		if err != nil {
			return err
		}
		if err := validateRecoveredSitePaths(txn, webRoot, mailRoot); err != nil {
			return err
		}
		if txn.RecoveryRequired == "" && (txn.State == "committed" || txn.State == "rolled-back") && txn.UpdatedAt.Before(cutoff) {
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
		}
	}
	return nil
}

func recoveryConfiguredRoots(roots []string) (webRoot, mailRoot string) {
	if len(roots) > 0 {
		webRoot = roots[0]
	}
	if len(roots) > 1 {
		mailRoot = roots[1]
	}
	return webRoot, mailRoot
}

// validateRecoveredSitePaths binds persisted journal paths back to the
// configured ownership roots before recovery performs any rename or removal.
// The optional roots preserve isolated unit-test callers that intentionally
// use arbitrary temporary paths; production startup always supplies both.
func validateRecoveredSitePaths(txn *SiteTransaction, webRoot, mailRoot string) error {
	if webRoot != "" {
		expectedHome, err := safePath(webRoot, "sites", txn.Site, "public")
		if err != nil {
			return fmt.Errorf("validate recovery site root: %w", err)
		}
		if filepath.Clean(txn.Home) != filepath.Clean(expectedHome) {
			return errors.New("recovery transaction site path is outside the configured web root")
		}
	}
	if txn.MailRoot != "" {
		if mailRoot == "" {
			return errors.New("recovery transaction mail path has no configured mail root")
		}
		expectedMail, err := safePath(mailRoot, txn.Site)
		if err != nil {
			return fmt.Errorf("validate recovery mail root: %w", err)
		}
		if filepath.Clean(txn.MailRoot) != filepath.Clean(expectedMail) {
			return errors.New("recovery transaction mail path is outside the configured mail root")
		}
	}
	return nil
}
