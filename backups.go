package stepanel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cyberducttape/StePanel/internal/backup"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Type aliases for backward compatibility
type BackupEntry = backup.BackupEntry
type BackupManifest = backup.BackupManifest
type BackupResult = backup.BackupResult

const maxBackupBytes = backup.MaxBackupBytes

// backupVerificationCache stores recent verification results for backup listing
// only. Restore and other trust-sensitive paths must use VerifySiteBackupStrict,
// which never consults this cache.
//
// Key: backup directory path
// Value: verification result + timestamp
// Cache TTL: 5 minutes
// Eviction: oldest entries when cache exceeds 1000 entries
type verificationCacheEntry struct {
	VerifiedAt  time.Time
	Consistency string
	Checksum    string
	CachedAt    time.Time
}

var (
	backupVerificationCache = make(map[string]verificationCacheEntry)
	backupCacheMu           sync.Mutex // Protects concurrent access to cache
	verificationCacheTTL    = 5 * time.Minute
	maxCacheEntries         = 1000
)

func (a *App) backups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if !a.requireCustomerScope(w, r, "backup:read") {
			return
		}
		site := strings.TrimSpace(r.URL.Query().Get("site"))
		if site != "" {
			site = safeUser(site)
			if site == "" {
				http.Error(w, "invalid site", http.StatusUnprocessableEntity)
				return
			}
		}
		limit := 100
		if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
			parsed, parseErr := strconv.Atoi(rawLimit)
			if parseErr != nil || parsed < 1 || parsed > 500 {
				http.Error(w, "limit must be between 1 and 500", http.StatusUnprocessableEntity)
				return
			}
			limit = parsed
		}
		if !a.Auth.IsAdministrator(r) {
			username := a.Auth.UsernameForRequest(r)
			if site == "" {
				backups := []BackupResult{}
				if a.Accounts != nil {
					assignedSites, err := a.Accounts.GetSitesWithError(username)
					if err != nil {
						http.Error(w, "unable to inspect site assignments", http.StatusInternalServerError)
						return
					}
					for _, assignedSite := range assignedSites {
						access, _ := a.authorizeSite(r, assignedSite)
						items, err := listBackupsPage(a.Config.BackupRoot, access, limit, a.Config.backupVerificationKeys()...)
						if err != nil {
							http.Error(w, "unable to inspect backups", http.StatusInternalServerError)
							return
						}
						backups = append(backups, items...)
					}
				}
				if len(backups) > limit {
					backups = backups[:limit]
				}
				writeJSON(w, http.StatusOK, map[string]any{"backups": backups})
				return
			}
			access, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", http.StatusForbidden)
			if !ok {
				return
			}
			manifests, err := listBackupsPage(a.Config.BackupRoot, access, limit, a.Config.backupVerificationKeys()...)
			if err != nil {
				http.Error(w, "unable to inspect backups", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"backups": manifests})
			return
		}
		manifests, err := listBackupsPageUnscoped(a.Config.BackupRoot, site, limit, a.Config.backupVerificationKeys()...)
		if err != nil {
			http.Error(w, "unable to inspect backups", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"backups": manifests})
	case http.MethodPost:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		operationKey, err := requestOperationKey(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		var input struct {
			Site             string `json:"site"`
			IncludeDatabases bool   `json:"include_databases"`
		}
		if err := decodeJSON(w, r, 4096, &input); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		input.Site = safeUser(input.Site)
		if input.Site == "" {
			http.Error(w, "invalid site", http.StatusUnprocessableEntity)
			return
		}
		if _, ok := a.requireSiteAccess(w, r, input.Site, "site is not assigned to this account", http.StatusForbidden); !ok {
			return
		}
		if !a.Auth.HasRequiredCustomerScope(r, "backup:create") {
			http.Error(w, "API token lacks the backup:create scope", http.StatusForbidden)
			return
		}
		if input.IncludeDatabases && a.Config.DBCtl == "" {
			http.Error(w, "managed database backup requires the local database helper", http.StatusUnprocessableEntity)
			return
		}
		publicRoot, pathErr := safePath(a.Config.WebRoot, "sites", input.Site, "public")
		if pathErr != nil {
			http.Error(w, "invalid site", http.StatusUnprocessableEntity)
			return
		}
		if info, err := os.Stat(publicRoot); err != nil || !info.IsDir() {
			http.Error(w, "site document root does not exist", http.StatusUnprocessableEntity)
			return
		}
		if err := os.MkdirAll(a.Config.BackupRoot, 0750); err != nil {
			http.Error(w, "backup root is unavailable", http.StatusInternalServerError)
			return
		}
		if free, err := availableBytes(a.Config.BackupRoot); err == nil && free < a.Config.MinFreeBytes {
			http.Error(w, "insufficient free space for backup", http.StatusInsufficientStorage)
			return
		}
		payload, err := json.Marshal(durableBackupRequest{Site: input.Site, IncludeDatabases: input.IncludeDatabases, Actor: a.Auth.UsernameForRequest(r)})
		if err != nil {
			http.Error(w, "could not encode backup job", http.StatusInternalServerError)
			return
		}
		job, _, err := a.Jobs.EnqueueIdempotent("site.backup", input.Site, operationKey, payload, 3)
		if err != nil {
			http.Error(w, "could not persist backup job", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": filepath.Join("/api/jobs", job.ID)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func CreateSiteBackup(cfg Config, site SiteCapability, includeDatabases bool) (result BackupResult, returnErr error) {
	return CreateSiteBackupContext(context.Background(), cfg, site, includeDatabases)
}

// CreateSiteBackupContext creates a verified backup while honoring ctx at
// every filesystem and publication boundary. Lock-protected callers must use
// this form so a fenced mutation lease cannot continue publishing a backup
// after another process has taken over the resource.
func CreateSiteBackupContext(ctx context.Context, cfg Config, site SiteCapability, includeDatabases bool) (result BackupResult, returnErr error) {
	return createSiteBackupContext(ctx, cfg, site, includeDatabases, nil)
}

func createSiteBackupContext(ctx context.Context, cfg Config, site SiteCapability, includeDatabases bool, ledger *capacityLedger) (result BackupResult, returnErr error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	siteName := site.Site()
	if safeUser(siteName) == "" {
		return result, errors.New("invalid backup site")
	}
	publicRoot, err := safePath(cfg.WebRoot, "sites", siteName, "public")
	if err != nil {
		return result, err
	}
	siteBytes, err := backup.ValidateTree(publicRoot, maxBackupBytes)
	if err != nil {
		return result, fmt.Errorf("validate backup tree: %w", err)
	}
	if err := os.MkdirAll(cfg.BackupRoot, 0750); err != nil {
		return result, err
	}
	tempDir, err := os.MkdirTemp(cfg.BackupRoot, ".backup-")
	if err != nil {
		return result, err
	}
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(tempDir)
		}
	}()
	if err := os.Chmod(tempDir, 0700); err != nil {
		return result, err
	}
	reservation, err := reserveBackupCapacity(cfg, tempDir, includeDatabases, ledger, siteBytes)
	if err != nil {
		return result, err
	}
	defer reservation.release()
	quiesced, resume, err := quiesceWordPressForBackup(ctx, cfg, siteName, publicRoot)
	if err != nil {
		return result, err
	}
	defer func() {
		if resumeErr := resume(); returnErr == nil && resumeErr != nil {
			returnErr = fmt.Errorf("resume WordPress after backup: %w", resumeErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := failureInjection("backup", "init"); err != nil {
		return result, err
	}
	processKillInjection("backup", "init")
	archivePath := filepath.Join(tempDir, "backup.tar.gz")
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	gz := gzip.NewWriter(archive)
	tw := tar.NewWriter(gz)
	consistency := backup.ConsistencyCrashConsistent
	if quiesced {
		consistency = backup.ConsistencyApplicationQuiesced
	}
	manifest := BackupManifest{Version: 1, Site: siteName, CreatedAt: time.Now().UTC(), Archive: "backup.tar.gz", Databases: []string{}, Entries: []BackupEntry{}, Consistency: consistency, ApplicationQuiesced: quiesced}
	var uncompressedBytes int64
	closeArchive := func() error {
		if err := tw.Close(); err != nil {
			_ = gz.Close()
			_ = archive.Close()
			return err
		}
		if err := gz.Close(); err != nil {
			_ = archive.Close()
			return err
		}
		if err := archive.Sync(); err != nil {
			_ = archive.Close()
			return err
		}
		return archive.Close()
	}
	abortArchive := func(cause error) error {
		if closeErr := closeArchive(); closeErr != nil {
			return errors.Join(cause, fmt.Errorf("finalize backup archive: %w", closeErr))
		}
		return cause
	}
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = 1000000
	}
	if err := addBackupTreeContext(ctx, tw, publicRoot, "site/public", maxEntries, &uncompressedBytes, &manifest); err != nil {
		return result, abortArchive(err)
	}
	if includeDatabases {
		if err := ctx.Err(); err != nil {
			return result, abortArchive(err)
		}
		databases, err := managedDatabasesForSiteContext(ctx, cfg, siteName)
		if err != nil {
			return result, abortArchive(err)
		}
		for _, database := range databases {
			if err := ctx.Err(); err != nil {
				return result, abortArchive(err)
			}
			if len(manifest.Entries) >= maxEntries {
				return result, abortArchive(errors.New("backup contains too many entries"))
			}
			dumpPath := filepath.Join(tempDir, database+".sql")
			if err := dumpManagedDatabaseContext(ctx, cfg, database, dumpPath); err != nil {
				return result, abortArchive(err)
			}
			if info, statErr := os.Stat(dumpPath); statErr == nil {
				measured := uint64(info.Size())
				if measured > backupDatabaseReservation {
					if err := reservation.grow(tempDir, measured-backupDatabaseReservation); err != nil {
						return result, abortArchive(err)
					}
				}
			} else if statErr != nil {
				return result, abortArchive(statErr)
			}
			if err := addBackupFileExpectedContext(ctx, tw, dumpPath, "databases/"+database+".sql", &uncompressedBytes, &manifest, nil); err != nil {
				return result, abortArchive(err)
			}
			if err := os.Remove(dumpPath); err != nil {
				return result, abortArchive(err)
			}
			manifest.Databases = append(manifest.Databases, database)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, abortArchive(err)
	}
	if err := closeArchive(); err != nil {
		return result, err
	}
	processKillInjection("backup", "archive")
	if err := failureInjection("backup", "archive"); err != nil {
		return result, err
	}
	archiveInfo, err := os.Stat(archivePath)
	if err != nil {
		return result, err
	}
	manifest.Bytes = archiveInfo.Size()
	manifest.ArchiveSHA256, err = fileSHA256(archivePath)
	if err != nil {
		return result, err
	}
	if err := failureInjection("backup", "verify"); err != nil {
		return result, err
	}
	processKillInjection("backup", "verify")
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := VerifyBackupArchive(archivePath, manifest); err != nil {
		return result, fmt.Errorf("verify completed backup: %w", err)
	}
	if cfg.BackupEncryptionKey != "" {
		encryptedPath := filepath.Join(tempDir, "backup.tar.gz.enc")
		keyID, err := encryptBackupArchive(archivePath, encryptedPath, cfg.BackupEncryptionKey)
		if err != nil {
			return result, fmt.Errorf("encrypt backup archive: %w", err)
		}
		if err := os.Remove(archivePath); err != nil {
			return result, fmt.Errorf("remove plaintext backup archive: %w", err)
		}
		archivePath = encryptedPath
		manifest.Archive = "backup.tar.gz.enc"
		manifest.Encryption = backupEncryptionName
		manifest.EncryptionKeyID = keyID
		archiveInfo, err = os.Stat(archivePath)
		if err != nil {
			return result, err
		}
		manifest.Bytes = archiveInfo.Size()
		manifest.ArchiveSHA256, err = fileSHA256(archivePath)
		if err != nil {
			return result, err
		}
		if err := VerifyBackupArchiveWithKey(archivePath, manifest, cfg.backupDecryptionKeys()); err != nil {
			return result, fmt.Errorf("verify encrypted backup: %w", err)
		}
	}
	manifest.VerifiedAt = time.Now().UTC()
	manifest.ArchiveVerified = true
	manifest.DatabaseDumpVerified = len(manifest.Databases) > 0
	if err := writeBackupManifest(tempDir, manifest, cfg.BackupSigningKey); err != nil {
		return result, err
	}
	if err := writeSyncedFile(tempDir, manifest.Archive+".sha256", []byte(manifest.ArchiveSHA256+"  "+manifest.Archive+"\n"), 0600); err != nil {
		return result, err
	}
	if err := syncDirectory(tempDir); err != nil {
		return result, err
	}
	finalName := manifest.CreatedAt.Format("20060102-150405.000000000") + "-" + siteName
	finalPath := filepath.Join(cfg.BackupRoot, finalName)
	if err := failureInjection("backup", "commit"); err != nil {
		return result, err
	}
	processKillInjection("backup", "commit")
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := publishStagedDirectory(tempDir, finalPath, cfg.BackupRoot); err != nil {
		return result, fmt.Errorf("publish backup: %w", err)
	}
	result = BackupResult{Site: siteName, Path: finalPath, ArchiveSHA256: manifest.ArchiveSHA256, Bytes: manifest.Bytes, Databases: manifest.Databases, CreatedAt: manifest.CreatedAt, VerifiedAt: manifest.VerifiedAt, Consistency: manifest.Consistency, ManifestSigned: cfg.BackupSigningKey != "", Encrypted: manifest.Encryption != ""}
	return result, nil
}

const backupDatabaseReservation = 64 << 20

func reserveBackupCapacity(cfg Config, stagingPath string, includeDatabases bool, ledger *capacityLedger, siteBytes int64) (*capacityReservation, error) {
	if ledger == nil {
		return nil, nil
	}
	estimate := saturatingAdd(uint64(siteBytes), 1<<20)
	if cfg.BackupEncryptionKey != "" {
		estimate = saturatingAdd(estimate, uint64(siteBytes))
	}
	if includeDatabases {
		estimate = saturatingAdd(estimate, backupDatabaseReservation)
	}
	return ledger.reserve(cfg, "backup", stagingPath, []capacityDemand{{Path: stagingPath, Bytes: estimate}})
}

// quiesceWordPressForBackup uses the site's existing mutation lock together
// with WordPress maintenance mode when both wp-cli and WordPress are present.
// Non-WordPress sites retain the explicit crash-consistent contract. wp-cli
// runs as the site's isolated user (see runWordPress).
func quiesceWordPressForBackup(parent context.Context, cfg Config, site, publicRoot string) (bool, func() error, error) {
	noop := func() error { return nil }
	if cfg.WPCLI == "" || !commandAvailable(cfg.WPCLI) {
		return false, noop, nil
	}
	if _, err := os.Stat(filepath.Join(publicRoot, "wp-config.php")); err != nil {
		if os.IsNotExist(err) {
			return false, noop, nil
		}
		return false, noop, err
	}
	run := func(action string) error {
		_, err := runWordPress(parent, cfg, rootbroker.WordPressRequest{Action: action, Site: site})
		return err
	}
	status, err := runWordPress(parent, cfg, rootbroker.WordPressRequest{Action: "maintenance-status", Site: site})
	if err != nil {
		// A broken WordPress install must not block its own backup; the
		// manifest records the weaker crash-consistent guarantee instead.
		log.Printf("backup of %s is crash-consistent: WordPress maintenance-mode state unavailable: %v", site, err)
		return false, noop, nil
	}
	if status.Active {
		return true, noop, nil
	}
	if err := run("maintenance-activate"); err != nil {
		log.Printf("backup of %s is crash-consistent: %v", site, err)
		return false, noop, nil
	}
	return true, func() error { return run("maintenance-deactivate") }, nil
}

func addBackupTree(tw *tar.Writer, root, prefix string, maxEntries int, totalBytes *int64, manifest *BackupManifest) error {
	return addBackupTreeContext(context.Background(), tw, root, prefix, maxEntries, totalBytes, manifest)
}

func addBackupTreeContext(ctx context.Context, tw *tar.Writer, root, prefix string, maxEntries int, totalBytes *int64, manifest *BackupManifest) error {
	entries := 0
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		entries++
		if entries > maxEntries {
			return errors.New("backup contains too many filesystem entries")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup refuses symlink %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := prefix
		if rel != "." {
			name = filepath.ToSlash(filepath.Join(prefix, rel))
		}
		if info.IsDir() {
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = name + "/"
			header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
			return tw.WriteHeader(header)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("backup refuses special file %s", path)
		}
		// Keep the directory walk's inode identity through to the open. Site
		// files can be changed by their owning account while a backup is in
		// progress; accepting a replacement here would defeat the no-follow
		// validation performed above.
		return addBackupFileExpectedContext(ctx, tw, path, name, totalBytes, manifest, info)
	})
}

// addBackupFileExpected writes a regular file that still matches the file
// observed during validation. Callers without a preceding walk, such as the
// database dump staging path, pass a nil expected FileInfo.
func addBackupFileExpected(tw *tar.Writer, source, name string, totalBytes *int64, manifest *BackupManifest, expected os.FileInfo) error {
	return addBackupFileExpectedContext(context.Background(), tw, source, name, totalBytes, manifest, expected)
}

func addBackupFileExpectedContext(ctx context.Context, tw *tar.Writer, source, name string, totalBytes *int64, manifest *BackupManifest, expected os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, info, err := openRegularNoFollow(source, expected)
	if err != nil {
		return err
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > 2<<30 {
		return fmt.Errorf("backup entry %s exceeds the 2 GiB restore limit", name)
	}
	if *totalBytes+info.Size() > maxBackupBytes {
		return errors.New("backup exceeds the 20 GiB restore limit")
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(name)
	header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	hash := sha256.New()
	written, err := io.CopyN(io.MultiWriter(tw, hash), contextReader{ctx: ctx, reader: file}, info.Size())
	if err != nil {
		return err
	}
	manifest.Entries = append(manifest.Entries, BackupEntry{Path: header.Name, Size: written, SHA256: hex.EncodeToString(hash.Sum(nil))})
	*totalBytes += written
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func managedDatabasesForSite(cfg Config, site string) ([]string, error) {
	return managedDatabasesForSiteContext(context.Background(), cfg, site)
}

func managedDatabasesForSiteContext(parent context.Context, cfg Config, site string) ([]string, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, helperConfigMutationTimeout)
	defer cancel()
	var output []byte
	var err error
	if cfg.Production {
		client, clientErr := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
		if clientErr != nil {
			return nil, fmt.Errorf("create database broker client: %w", clientErr)
		}
		response, executeErr := client.DBInventory(ctx)
		if executeErr != nil {
			return nil, fmt.Errorf("database inventory through root broker: %w", executeErr)
		}
		if !response.OK {
			return nil, errors.New(response.Error)
		}
		var details rootbroker.DBResponse
		if decodeErr := json.Unmarshal(response.Details, &details); decodeErr != nil {
			return nil, fmt.Errorf("decode database inventory response: %w", decodeErr)
		}
		output = []byte(details.Output)
	} else {
		output, err, _ = runAllowlistedHelperOutput(ctx, cfg, nil, cfg.DBCtl, "list", site)
	}
	if err != nil {
		return nil, fmt.Errorf("list managed databases: %w: %s", err, strings.TrimSpace(string(output)))
	}
	databases := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		name := fields[0]
		if cfg.Production && (len(fields) < 2 || fields[1] != site) {
			continue
		}
		if !validManagedDatabaseIdentifier(name, 64) {
			return nil, errors.New("database helper returned an invalid managed database")
		}
		databases = append(databases, name)
	}
	sort.Strings(databases)
	return databases, nil
}

func createDatabaseSafetyBackupContext(ctx context.Context, cfg Config, database string) (DatabaseSafetyBackup, error) {
	result := DatabaseSafetyBackup{Database: database, Created: time.Now().UTC()}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	root := filepath.Join(cfg.BackupRoot, ".database-deletions")
	if err := os.MkdirAll(root, 0700); err != nil {
		return result, err
	}
	temp, err := os.MkdirTemp(root, ".pending-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(temp)
	dump := filepath.Join(temp, database+".sql")
	if err := dumpManagedDatabaseContext(ctx, cfg, database, dump); err != nil {
		return result, err
	}
	result.SHA256, err = fileSHA256(dump)
	if err != nil {
		return result, err
	}
	if err := writeSyncedFile(temp, database+".sql.sha256", []byte(result.SHA256+"  "+database+".sql\n"), 0600); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// The database name remains in the signed result metadata. Keep it out of
	// the filesystem destination name so the final rename depends only on a
	// timestamp and a locally generated request identifier.
	finalName := result.Created.Format("20060102-150405.000000000") + "-" + newRequestID()
	final, err := safePath(root, finalName)
	if err != nil {
		return result, fmt.Errorf("invalid database safety backup path: %w", err)
	}
	if err := syncDirectory(temp); err != nil {
		return result, err
	}
	if err := publishStagedDirectory(temp, final, root); err != nil {
		return result, fmt.Errorf("publish database safety backup: %w", err)
	}
	result.Path = final
	return result, nil
}

func dumpManagedDatabaseContext(parent context.Context, cfg Config, database, destination string) error {
	if !validManagedDatabaseIdentifier(database, 64) {
		return errors.New("invalid managed database")
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, helperBackupRestoreTimeout)
	defer cancel()
	if cfg.Production {
		// Production has no sudo: the root broker streams the dump into the
		// empty file created above, which it verifies before writing.
		if err := out.Close(); err != nil {
			return err
		}
		destination, err := filepath.Abs(destination)
		if err != nil {
			return err
		}
		client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
		if err != nil {
			return err
		}
		response, err := client.DBDumpToPath(ctx, database, destination)
		if err != nil {
			return fmt.Errorf("dump managed database %s: %w", database, err)
		}
		if !response.OK {
			return fmt.Errorf("dump managed database %s: %s", database, response.Error)
		}
		return nil
	}
	cmd := helperCommandContext(ctx, cfg, cfg.DBCtl, "dump", database)
	var stderr strings.Builder
	cmd.Stdout = out
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr == nil {
		runErr = out.Sync()
	}
	if closeErr := out.Close(); runErr == nil {
		runErr = closeErr
	}
	if runErr != nil {
		return fmt.Errorf("dump managed database %s: %w: %s", database, runErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, _, err := openRegularNoFollow(path, nil)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// getBackupVerificationFromCache returns cached verification result if valid
func getBackupVerificationFromCache(path string) (verificationCacheEntry, bool) {
	backupCacheMu.Lock()
	defer backupCacheMu.Unlock()

	entry, ok := backupVerificationCache[path]
	if !ok {
		return entry, false
	}
	// Check if cache entry is still valid (TTL)
	if time.Since(entry.CachedAt) > verificationCacheTTL {
		delete(backupVerificationCache, path)
		return entry, false
	}
	return entry, true
}

// setBackupVerificationCache stores verification result in cache
func setBackupVerificationCache(path, consistency, checksum string) {
	backupCacheMu.Lock()
	defer backupCacheMu.Unlock()

	// Simple LRU-ish eviction: delete oldest if cache is full
	if len(backupVerificationCache) >= maxCacheEntries {
		var oldest string
		var oldestTime time.Time
		for k, v := range backupVerificationCache {
			if oldestTime.IsZero() || v.CachedAt.Before(oldestTime) {
				oldest = k
				oldestTime = v.CachedAt
			}
		}
		if oldest != "" {
			delete(backupVerificationCache, oldest)
		}
	}

	backupVerificationCache[path] = verificationCacheEntry{
		VerifiedAt:  time.Now(),
		Consistency: consistency,
		Checksum:    checksum,
		CachedAt:    time.Now(),
	}
}

func VerifyBackupArchive(path string, manifest BackupManifest) error {
	return VerifyBackupArchiveWithKey(path, manifest, nil)
}

// VerifyBackupArchiveWithKey verifies an archive against its manifest. keys
// holds every configured backup encryption key, current first; encrypted
// archives are decrypted with whichever one the archive names.
func VerifyBackupArchiveWithKey(path string, manifest BackupManifest, keys []string) error {
	if manifest.Version != 1 || safeUser(manifest.Site) == "" || (manifest.Archive != "backup.tar.gz" && manifest.Archive != "backup.tar.gz.enc") || len(manifest.ArchiveSHA256) != sha256.Size*2 {
		return errors.New("invalid backup manifest metadata")
	}
	if manifest.Archive == "backup.tar.gz.enc" {
		if !isBackupEncryptionName(manifest.Encryption) || !hasBackupKey(keys) {
			return errors.New("encrypted backup requires its encryption key")
		}
	} else if manifest.Encryption != "" || manifest.EncryptionKeyID != "" {
		return errors.New("plaintext backup has unexpected encryption metadata")
	}
	file, info, err := openRegularNoFollow(path, nil)
	if err != nil {
		return err
	}
	defer file.Close()
	if !info.Mode().IsRegular() || info.Size() != manifest.Bytes {
		return errors.New("archive size does not match manifest")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash backup archive: %w", err)
	}
	archiveHash := hex.EncodeToString(hash.Sum(nil))
	if archiveHash != manifest.ArchiveSHA256 {
		return errors.New("archive checksum does not match manifest")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind backup archive: %w", err)
	}
	if manifest.Archive == "backup.tar.gz.enc" {
		return withDecryptedBackupArchiveReader(file, keys, manifest.Encryption, func(archivePath string) error {
			plaintext, _, err := openRegularNoFollow(archivePath, nil)
			if err != nil {
				return err
			}
			defer plaintext.Close()
			return verifyBackupArchiveContents(plaintext, manifest)
		})
	}
	return verifyBackupArchiveContents(file, manifest)
}

func verifyBackupArchiveContents(input io.Reader, manifest BackupManifest) error {
	gz, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer gz.Close()
	expected := make(map[string]BackupEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if _, err := hex.DecodeString(entry.SHA256); err != nil || len(entry.SHA256) != sha256.Size*2 {
			return errors.New("manifest contains an invalid entry checksum")
		}
		if _, exists := expected[entry.Path]; exists || !safeArchivePath(entry.Path) || entry.Size < 0 || entry.Size > maxBackupBytes {
			return errors.New("manifest contains a duplicate or unsafe entry")
		}
		expected[entry.Path] = entry
	}
	seen := make(map[string]bool, len(expected))
	tr := tar.NewReader(gz)
	var total int64
	for count := 0; ; count++ {
		if count > 1000000 {
			return errors.New("backup archive contains too many entries")
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if !safeArchivePath(header.Name) {
			return errors.New("backup archive contains an unsafe path")
		}
		if header.FileInfo().IsDir() {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return errors.New("backup archive contains an unsupported entry type")
		}
		if header.Size < 0 || header.Size > maxBackupBytes {
			return errors.New("backup archive exceeds the verification limit")
		}
		entry, ok := expected[header.Name]
		if !ok || seen[header.Name] || entry.Size != header.Size {
			return fmt.Errorf("backup entry %s is unexpected or has the wrong size", header.Name)
		}
		if total > maxBackupBytes-header.Size {
			return errors.New("backup archive exceeds the verification limit")
		}
		total += header.Size
		hash := sha256.New()
		if copied, err := io.Copy(hash, tr); err != nil {
			return fmt.Errorf("read backup entry %s: %w", header.Name, err)
		} else if copied != header.Size {
			return fmt.Errorf("read backup entry %s: short entry (got %d bytes, want %d)", header.Name, copied, header.Size)
		}
		if hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return fmt.Errorf("backup entry %s checksum mismatch", header.Name)
		}
		seen[header.Name] = true
	}
	if len(seen) != len(expected) {
		return errors.New("backup archive is missing manifest entries")
	}
	return nil
}

func writeBackupManifest(root string, manifest BackupManifest, signingKey ...string) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open backup manifest directory: %w", err)
	}
	defer dir.Close()
	key := ""
	if len(signingKey) > 0 {
		key = signingKey[0]
	}
	if key != "" {
		manifest.SignatureAlgorithm = "HMAC-SHA256"
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(root, ".manifest-*.tmp")
	if err != nil {
		return err
	}
	tempName := filepath.Base(temp.Name())
	defer dir.Remove(tempName)
	// Set restrictive permissions immediately to protect backup manifest
	if err = temp.Chmod(0600); err != nil {
		temp.Close()
		return fmt.Errorf("secure backup manifest file permissions: %w", err)
	}
	// Write manifest data with proper permissions guaranteed
	payload := append(data, '\n')
	if written, writeErr := temp.Write(payload); writeErr != nil {
		temp.Close()
		return fmt.Errorf("write backup manifest: %w", writeErr)
	} else if written != len(payload) {
		temp.Close()
		return fmt.Errorf("write backup manifest: %w", io.ErrShortWrite)
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync backup manifest: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close backup manifest: %w", err)
	}
	if err := dir.Rename(tempName, "manifest.json"); err != nil {
		return err
	}
	if key != "" {
		signedData := append(append([]byte(nil), data...), '\n')
		if err := writeSyncedFile(root, "manifest.sig", []byte(backupManifestSignature(signedData, key)+"\n"), 0600); err != nil {
			return err
		}
	}
	return nil
}

func writeSyncedFile(directory, name string, data []byte, mode os.FileMode) error {
	if !filepath.IsLocal(name) || filepath.Base(name) != name {
		return fmt.Errorf("refusing non-local synced file name %q", name)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("open synced-file directory: %w", err)
	}
	defer root.Close()
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if written, writeErr := file.Write(data); writeErr != nil {
		err = writeErr
	} else if written != len(data) {
		err = io.ErrShortWrite
	} else {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// publishSyncDirectory is the parent-directory fsync used by
// publishStagedDirectory; tests replace it to inject I/O failures.
var publishSyncDirectory = syncDirectory

// PublishIndeterminateError reports that a staged directory was renamed to
// its final name, the parent directory could not be fsynced, and the rename
// could not be reversed. The published content is complete and still present
// at Path, but its directory entry may not survive power loss. Callers must
// surface this state instead of reporting a clean failure.
type PublishIndeterminateError struct {
	Path        string
	SyncErr     error
	RollbackErr error
}

func (e *PublishIndeterminateError) Error() string {
	return fmt.Sprintf("%s was published but is not durable (parent sync: %v) and could not be withdrawn (rollback: %v)", e.Path, e.SyncErr, e.RollbackErr)
}

func (e *PublishIndeterminateError) Unwrap() []error {
	return []error{e.SyncErr, e.RollbackErr}
}

// publishStagedDirectory renames a fully synced staged directory to final
// and fsyncs parent so the new directory entry is durable. When the parent
// fsync fails the rename is reversed so a failed result never leaves a
// half-committed publication behind; if the reversal itself fails, a
// *PublishIndeterminateError names the path that remains published.
func publishStagedDirectory(staged, final, parent string) error {
	if err := os.Rename(staged, final); err != nil {
		return err
	}
	syncErr := publishSyncDirectory(parent)
	if syncErr == nil {
		return nil
	}
	if rollbackErr := os.Rename(final, staged); rollbackErr != nil {
		return &PublishIndeterminateError{Path: final, SyncErr: syncErr, RollbackErr: rollbackErr}
	}
	if err := publishSyncDirectory(parent); err != nil {
		return fmt.Errorf("sync parent directory: %w (rollback rename is also not durable: %v)", syncErr, err)
	}
	return fmt.Errorf("sync parent directory: %w", syncErr)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	if closeErr := directory.Close(); err == nil {
		err = closeErr
	}
	return err
}

func readBackupManifest(root string) (BackupManifest, error) {
	path, err := safePath(root, "manifest.json")
	if err != nil {
		return BackupManifest{}, err
	}
	file, info, err := openRegularNoFollow(path, nil)
	if err != nil {
		return BackupManifest{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<20 {
		_ = file.Close()
		return BackupManifest{}, errors.New("backup manifest is not a bounded regular file")
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr == nil {
		readErr = closeErr
	}
	if readErr != nil {
		return BackupManifest{}, readErr
	}
	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Version != 1 || safeUser(manifest.Site) == "" || (manifest.Archive != "backup.tar.gz" && manifest.Archive != "backup.tar.gz.enc") || manifest.VerifiedAt.IsZero() || manifest.Bytes < 0 || len(manifest.ArchiveSHA256) != sha256.Size*2 {
		return BackupManifest{}, errors.New("invalid backup manifest")
	}
	if _, err := hex.DecodeString(manifest.ArchiveSHA256); err != nil {
		return BackupManifest{}, errors.New("invalid backup archive checksum")
	}
	archivePath, err := safePath(root, manifest.Archive)
	if err != nil {
		return BackupManifest{}, errors.New("backup archive path escapes backup root")
	}
	archiveInfo, err := os.Stat(archivePath)
	if err != nil || !archiveInfo.Mode().IsRegular() || archiveInfo.Size() != manifest.Bytes {
		return BackupManifest{}, errors.New("backup archive is missing or does not match its manifest")
	}
	return manifest, nil
}

func backupManifestSignature(data []byte, key string) string {
	derived := sha256.Sum256([]byte(key))
	h := hmac.New(sha256.New, derived[:])
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func verifyBackupManifestSignature(root string, data []byte, manifest BackupManifest, signingKey string) error {
	signaturePath, err := safePath(root, "manifest.sig")
	if err != nil {
		return err
	}
	signatureFile, _, err := openRegularNoFollow(signaturePath, nil)
	if errors.Is(err, os.ErrNotExist) {
		if signingKey != "" || manifest.SignatureAlgorithm != "" {
			return errors.New("backup manifest signature is missing")
		}
		return nil
	}
	if err != nil {
		return err
	}
	signature, readErr := io.ReadAll(signatureFile)
	closeErr := signatureFile.Close()
	if readErr == nil {
		readErr = closeErr
	}
	if readErr != nil {
		return readErr
	}
	if signingKey == "" {
		return errors.New("backup manifest is signed but STEPANEL_BACKUP_SIGNING_KEY is unavailable")
	}
	provided, err := hex.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(provided) != sha256.Size {
		return errors.New("backup manifest signature is malformed")
	}
	expected, _ := hex.DecodeString(backupManifestSignature(data, signingKey))
	if !hmac.Equal(provided, expected) {
		return errors.New("backup manifest signature does not verify")
	}
	return nil
}

// VerifySiteBackupForListing verifies a backup for display in a backup listing.
// The archive verification result may be cached briefly to avoid re-hashing
// large archives on every list request. Call VerifySiteBackupStrict before any
// restore, rehearsal, migration, staging, or other operation that consumes the
// archive contents.
func VerifySiteBackupForListing(root string, signingKey string, encryptionKeys ...string) (BackupManifest, error) {
	return verifySiteBackup(root, true, signingKey, encryptionKeys...)
}

// VerifySiteBackupStrict always re-hashes and fully validates the archive. It
// deliberately bypasses the listing cache because a path-only cache cannot
// establish that the bytes being consumed are the bytes that were verified.
func VerifySiteBackupStrict(root string, signingKey string, encryptionKeys ...string) (BackupManifest, error) {
	return verifySiteBackup(root, false, signingKey, encryptionKeys...)
}

func verifySiteBackup(root string, allowCache bool, signingKey string, encryptionKeys ...string) (BackupManifest, error) {
	manifest, err := readBackupManifest(root)
	if err != nil {
		return BackupManifest{}, err
	}
	archivePath, err := safePath(root, manifest.Archive)
	if err != nil {
		return BackupManifest{}, err
	}

	if allowCache {
		// This cache is intentionally not keyed by file identity. It is safe only
		// for listing results and must never authorize archive consumption.
		if cached, ok := getBackupVerificationFromCache(archivePath); ok {
			manifest.Consistency = cached.Consistency
			manifest.ArchiveSHA256 = cached.Checksum
		} else {
			if err := VerifyBackupArchiveWithKey(archivePath, manifest, encryptionKeys); err != nil {
				return BackupManifest{}, err
			}
			setBackupVerificationCache(archivePath, manifest.Consistency, manifest.ArchiveSHA256)
		}
	} else if err := VerifyBackupArchiveWithKey(archivePath, manifest, encryptionKeys); err != nil {
		return BackupManifest{}, err
	}
	manifestPath, err := safePath(root, "manifest.json")
	if err != nil {
		return BackupManifest{}, err
	}
	manifestFile, _, err := openRegularNoFollow(manifestPath, nil)
	if err != nil {
		return BackupManifest{}, err
	}
	data, readErr := io.ReadAll(manifestFile)
	closeErr := manifestFile.Close()
	if readErr == nil {
		readErr = closeErr
	}
	err = readErr
	if err != nil {
		return BackupManifest{}, err
	}
	if err := verifyBackupManifestSignature(root, data, manifest, signingKey); err != nil {
		return BackupManifest{}, err
	}
	return manifest, nil
}

func firstOptional(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// backupKeysFromOptional returns the encryption keys that follow the signing
// key in the listing helpers' optional key arguments.
func backupKeysFromOptional(values []string) []string {
	if len(values) < 2 {
		return nil
	}
	return values[1:]
}

func listBackups(root string, signingKey ...string) ([]BackupResult, error) {
	return listBackupsPageUnscoped(root, "", 0, signingKey...)
}

// listBackupsPageUnscoped is an internal helper for listing backups without
// tenant scoping, used by the unscoped listBackups wrapper (test-only today)
// and by admin routes that need to list all sites' backups or an unscoped
// admin listing. Not exported; scoped listing uses listBackupsPage.
func listBackupsPageUnscoped(root, site string, limit int, signingKey ...string) ([]BackupResult, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []BackupResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	backups := []BackupResult{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path, err := safePath(root, entry.Name())
		if err != nil {
			log.Printf("skip backup entry outside backup root %s", entry.Name())
			continue
		}
		manifest, err := VerifySiteBackupForListing(path, firstOptional(signingKey), backupKeysFromOptional(signingKey)...)
		if err != nil {
			// A damaged artifact must not hide every healthy backup from the
			// operator. Keep it visible in logs for quarantine/repair workflows.
			log.Printf("skip invalid backup manifest %s: %v", entry.Name(), err)
			continue
		}
		if site != "" && manifest.Site != site {
			continue
		}
		backups = append(backups, BackupResult{Site: manifest.Site, Path: path, ArchiveSHA256: manifest.ArchiveSHA256, Bytes: manifest.Bytes, Databases: manifest.Databases, CreatedAt: manifest.CreatedAt, VerifiedAt: manifest.VerifiedAt, Consistency: manifest.Consistency, ManifestSigned: manifest.SignatureAlgorithm != "", Encrypted: manifest.Encryption != ""})
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].CreatedAt.After(backups[j].CreatedAt) })
	if limit > 0 && len(backups) > limit {
		backups = backups[:limit]
	}
	return backups, nil
}

// listBackupsPage lists backups for a specific site using a SiteCapability to
// enforce tenant scoping. Call this from scoped request handlers; use
// listBackupsPageUnscoped for admin unscoped listing (not exported).
func listBackupsPage(root string, site SiteCapability, limit int, signingKey ...string) ([]BackupResult, error) {
	siteName := site.Site()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []BackupResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	backups := []BackupResult{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path, err := safePath(root, entry.Name())
		if err != nil {
			log.Printf("skip backup entry outside backup root %s", entry.Name())
			continue
		}
		manifest, err := VerifySiteBackupForListing(path, firstOptional(signingKey), backupKeysFromOptional(signingKey)...)
		if err != nil {
			log.Printf("skip invalid backup manifest %s: %v", entry.Name(), err)
			continue
		}
		if manifest.Site != siteName {
			continue
		}
		backups = append(backups, BackupResult{Site: manifest.Site, Path: path, ArchiveSHA256: manifest.ArchiveSHA256, Bytes: manifest.Bytes, Databases: manifest.Databases, CreatedAt: manifest.CreatedAt, VerifiedAt: manifest.VerifiedAt, Consistency: manifest.Consistency, ManifestSigned: manifest.SignatureAlgorithm != "", Encrypted: manifest.Encryption != ""})
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].CreatedAt.After(backups[j].CreatedAt) })
	if limit > 0 && len(backups) > limit {
		backups = backups[:limit]
	}
	return backups, nil
}
