package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type SiteTransaction struct {
	Version      int               `json:"version"`
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Site         string            `json:"site"`
	Home         string            `json:"home"`
	Backup       string            `json:"backup"`
	FailedSite   string            `json:"failed_site,omitempty"`
	MailRoot     string            `json:"mail_root,omitempty"`
	MailBackup   string            `json:"mail_backup,omitempty"`
	MailExisting bool              `json:"mail_existing,omitempty"`
	HadExisting  bool              `json:"had_existing"`
	State        string            `json:"state"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Databases    []ManagedDatabase `json:"databases,omitempty"`

	dir string
}

type ManagedDatabase struct {
	Name string `json:"name"`
	User string `json:"user,omitempty"`
	Kind string `json:"kind"`
}

func BeginSiteTransaction(root, home, kind string, access SiteCapability) (*SiteTransaction, error) {
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
	home, err = filepath.Abs(home)
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
		if err := os.Rename(home, txn.Backup); err != nil {
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
	return txn, nil
}

func (t *SiteTransaction) Commit() error {
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

func (t *SiteTransaction) cleanupDatabases(cfg Config) error {
	for len(t.Databases) > 0 {
		database := t.Databases[0]
		var err error
		switch database.Kind {
		case "cpmove":
			err = dropDatabase(cfg, database.Name)
		case "wordpress":
			err = cleanupWPressDatabase(cfg, database.Name, database.User)
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
	if t.State == "committed" || t.State == "rolled-back" {
		return nil
	}
	if t.State == "rolling-back" && t.HadExisting {
		_, backupErr := os.Lstat(t.Backup)
		_, homeErr := os.Lstat(t.Home)
		if errors.Is(backupErr, os.ErrNotExist) && homeErr == nil {
			t.State = "rolled-back"
			return t.persist()
		}
	}
	t.State = "rolling-back"
	if err := t.persist(); err != nil {
		return fmt.Errorf("record rollback start: %w", err)
	}
	if _, err := os.Lstat(t.Home); err == nil {
		failed := filepath.Join(t.dir, "failed-site")
		if _, existsErr := os.Lstat(failed); existsErr == nil {
			failed = filepath.Join(t.dir, "failed-site-"+time.Now().UTC().Format("150405.000000000"))
		}
		if err := os.Rename(t.Home, failed); err != nil {
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
		if _, err := os.Lstat(t.Backup); err != nil {
			return fmt.Errorf("recovery backup is unavailable: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(t.Home), 0750); err != nil {
			return err
		}
		if err := os.Rename(t.Backup, t.Home); err != nil {
			return fmt.Errorf("restore previous site: %w", err)
		}
	}
	if t.MailRoot != "" {
		if _, err := os.Lstat(t.MailRoot); err == nil {
			failedMail := filepath.Join(t.dir, "failed-mail")
			if renameErr := os.Rename(t.MailRoot, failedMail); renameErr != nil {
				return fmt.Errorf("preserve failed mail tree: %w", renameErr)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect failed mail tree: %w", err)
		}
		if t.MailExisting {
			if _, err := os.Lstat(t.MailBackup); err != nil {
				return fmt.Errorf("mail recovery backup is unavailable: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(t.MailRoot), 0750); err != nil {
				return err
			}
			if err := os.Rename(t.MailBackup, t.MailRoot); err != nil {
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
	if _, err = tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write transaction state: %w", err)
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
	if dir, err := os.Open(t.dir); err == nil {
		syncErr := dir.Sync()
		_ = dir.Close()
		if syncErr != nil {
			return syncErr
		}
	}
	return nil
}

func RecoverSiteTransactions(root string) ([]string, error) {
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
		if err != nil {
			if quarantineErr := quarantineRecoveryTransaction(root, dir, err); quarantineErr != nil {
				failures = append(failures, fmt.Errorf("quarantine invalid transaction %s: %w (original: %v)", entry.Name(), quarantineErr, err))
			} else {
				failures = append(failures, fmt.Errorf("quarantined invalid transaction %s: %w", entry.Name(), err))
			}
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
		if err := txn.Rollback(); err != nil {
			failures = append(failures, fmt.Errorf("recover transaction %s: %w", txn.ID, err))
			continue
		}
		recovered = append(recovered, txn.ID)
	}
	sort.Strings(recovered)
	return recovered, errors.Join(failures...)
}

func RecoverTransactionDatabases(cfg Config, root string) ([]string, error) {
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
		if err != nil {
			if quarantineErr := quarantineRecoveryTransaction(root, filepath.Join(root, entry.Name()), err); quarantineErr != nil {
				failures = append(failures, fmt.Errorf("quarantine invalid transaction %s: %w (original: %v)", entry.Name(), quarantineErr, err))
			} else {
				failures = append(failures, fmt.Errorf("quarantined invalid transaction %s: %w", entry.Name(), err))
			}
			continue
		}
		if txn.State == "committed" || txn.State == "rolled-back" || len(txn.Databases) == 0 {
			continue
		}
		if err := txn.cleanupDatabases(cfg); err != nil {
			failures = append(failures, fmt.Errorf("recover transaction %s databases: %w", txn.ID, err))
			continue
		}
		recovered = append(recovered, txn.ID)
	}
	sort.Strings(recovered)
	return recovered, errors.Join(failures...)
}

// quarantineRecoveryTransaction removes malformed journal entries from the
// active recovery scan while preserving them for operator inspection.
func quarantineRecoveryTransaction(root, dir string, cause error) error {
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
	if txn.Version != 1 || txn.ID != filepath.Base(dir) || safeUser(txn.Site) == "" || !filepath.IsAbs(txn.Home) {
		return nil, fmt.Errorf("invalid recovery transaction %s", filepath.Base(dir))
	}
	for _, database := range txn.Databases {
		if err := validateManagedDatabase(database); err != nil {
			return nil, fmt.Errorf("recovery transaction %s contains an invalid managed database: %w", txn.ID, err)
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

func CleanupSiteTransactions(root string, maxAge time.Duration) error {
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
		if (txn.State == "committed" || txn.State == "rolled-back") && txn.UpdatedAt.Before(cutoff) {
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
		}
	}
	return nil
}
