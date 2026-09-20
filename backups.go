package main

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
	"github.com/itchyitchy123/StePanel/internal/backup"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Type aliases for backward compatibility
type BackupEntry = backup.BackupEntry
type BackupManifest = backup.BackupManifest
type BackupResult = backup.BackupResult

const maxBackupBytes = backup.MaxBackupBytes

func (a *App) backups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
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
					for _, assignedSite := range a.Accounts.GetSites(username) {
						access, _ := a.authorizeSite(r, assignedSite)
						items, err := listBackupsPage(a.Config.BackupRoot, access, limit, a.Config.BackupSigningKey)
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
			manifests, err := listBackupsPage(a.Config.BackupRoot, access, limit, a.Config.BackupSigningKey)
			if err != nil {
				http.Error(w, "unable to inspect backups", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"backups": manifests})
			return
		}
		manifests, err := listBackupsPageUnscoped(a.Config.BackupRoot, site, limit, a.Config.BackupSigningKey)
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
		publicRoot := filepath.Join(a.Config.WebRoot, "sites", input.Site, "public")
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
		job, _, err := a.Jobs.EnqueueIdempotent("site.backup", input.Site, "", payload, 3)
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
	siteName := site.Site()
	if safeUser(siteName) == "" {
		return result, errors.New("invalid backup site")
	}
	publicRoot := filepath.Join(cfg.WebRoot, "sites", siteName, "public")
	if err := ensureInside(cfg.WebRoot, publicRoot); err != nil {
		return result, err
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
	archivePath := filepath.Join(tempDir, "backup.tar.gz")
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	gz := gzip.NewWriter(archive)
	tw := tar.NewWriter(gz)
	manifest := BackupManifest{Version: 1, Site: siteName, CreatedAt: time.Now().UTC(), Archive: "backup.tar.gz", Databases: []string{}, Entries: []BackupEntry{}, Consistency: "crash-consistent / logical backup"}
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
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = 1000000
	}
	if err := addBackupTree(tw, publicRoot, "site/public", maxEntries, &uncompressedBytes, &manifest); err != nil {
		_ = closeArchive()
		return result, err
	}
	if includeDatabases {
		databases, err := managedDatabasesForSite(cfg, siteName)
		if err != nil {
			_ = closeArchive()
			return result, err
		}
		for _, database := range databases {
			if len(manifest.Entries) >= maxEntries {
				_ = closeArchive()
				return result, errors.New("backup contains too many entries")
			}
			dumpPath := filepath.Join(tempDir, database+".sql")
			if err := dumpManagedDatabase(cfg, database, dumpPath); err != nil {
				_ = closeArchive()
				return result, err
			}
			if err := addBackupFile(tw, dumpPath, "databases/"+database+".sql", &uncompressedBytes, &manifest); err != nil {
				_ = closeArchive()
				return result, err
			}
			if err := os.Remove(dumpPath); err != nil {
				_ = closeArchive()
				return result, err
			}
			manifest.Databases = append(manifest.Databases, database)
		}
	}
	if err := closeArchive(); err != nil {
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
	if err := VerifyBackupArchive(archivePath, manifest); err != nil {
		return result, fmt.Errorf("verify completed backup: %w", err)
	}
	manifest.VerifiedAt = time.Now().UTC()
	manifest.ArchiveVerified = true
	manifest.DatabaseDumpVerified = len(manifest.Databases) > 0
	if err := writeBackupManifest(tempDir, manifest, cfg.BackupSigningKey); err != nil {
		return result, err
	}
	if err := writeSyncedFile(filepath.Join(tempDir, "backup.tar.gz.sha256"), []byte(manifest.ArchiveSHA256+"  backup.tar.gz\n"), 0600); err != nil {
		return result, err
	}
	if err := syncDirectory(tempDir); err != nil {
		return result, err
	}
	finalName := manifest.CreatedAt.Format("20060102-150405.000000000") + "-" + siteName
	finalPath := filepath.Join(cfg.BackupRoot, finalName)
	if err := os.Rename(tempDir, finalPath); err != nil {
		return result, err
	}
	if err := syncDirectory(cfg.BackupRoot); err != nil {
		_ = os.Rename(finalPath, tempDir)
		return result, err
	}
	result = BackupResult{Site: siteName, Path: finalPath, ArchiveSHA256: manifest.ArchiveSHA256, Bytes: manifest.Bytes, Databases: manifest.Databases, VerifiedAt: manifest.VerifiedAt, Consistency: manifest.Consistency, ManifestSigned: cfg.BackupSigningKey != ""}
	return result, nil
}

func addBackupTree(tw *tar.Writer, root, prefix string, maxEntries int, totalBytes *int64, manifest *BackupManifest) error {
	entries := 0
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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
		return addBackupFileExpected(tw, path, name, totalBytes, manifest, info)
	})
}

func addBackupFile(tw *tar.Writer, source, name string, totalBytes *int64, manifest *BackupManifest) error {
	return addBackupFileExpected(tw, source, name, totalBytes, manifest, nil)
}

// addBackupFileExpected writes a regular file that still matches the file
// observed during validation. Callers without a preceding walk, such as the
// database dump staging path, can use addBackupFile instead.
func addBackupFileExpected(tw *tar.Writer, source, name string, totalBytes *int64, manifest *BackupManifest, expected os.FileInfo) error {
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
	written, err := io.CopyN(io.MultiWriter(tw, hash), file, info.Size())
	if err != nil {
		return err
	}
	manifest.Entries = append(manifest.Entries, BackupEntry{Path: header.Name, Size: written, SHA256: hex.EncodeToString(hash.Sum(nil))})
	*totalBytes += written
	return nil
}

func managedDatabasesForSite(cfg Config, site string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	output, err := runBoundedCommand(ctx, helperCommandContext(ctx, cfg, cfg.DBCtl, "list", site))
	if err != nil {
		return nil, fmt.Errorf("list managed databases: %w: %s", err, strings.TrimSpace(string(output)))
	}
	databases := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		name := strings.SplitN(line, "\t", 2)[0]
		if !validManagedDatabaseIdentifier(name, 64) {
			return nil, errors.New("database helper returned an invalid managed database")
		}
		databases = append(databases, name)
	}
	sort.Strings(databases)
	return databases, nil
}

func dumpManagedDatabase(cfg Config, database, destination string) error {
	if !validManagedDatabaseIdentifier(database, 64) {
		return errors.New("invalid managed database")
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
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
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func VerifyBackupArchive(path string, manifest BackupManifest) error {
	if manifest.Version != 1 || safeUser(manifest.Site) == "" || manifest.Archive != "backup.tar.gz" || len(manifest.ArchiveSHA256) != sha256.Size*2 {
		return errors.New("invalid backup manifest metadata")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != manifest.Bytes {
		return errors.New("archive size does not match manifest")
	}
	archiveHash, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if archiveHash != manifest.ArchiveSHA256 {
		return errors.New("archive checksum does not match manifest")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	expected := make(map[string]BackupEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if _, err := hex.DecodeString(entry.SHA256); err != nil || len(entry.SHA256) != sha256.Size*2 {
			return errors.New("manifest contains an invalid entry checksum")
		}
		if _, exists := expected[entry.Path]; exists || !safeArchivePath(entry.Path) || entry.Size < 0 {
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
		entry, ok := expected[header.Name]
		if !ok || seen[header.Name] || entry.Size != header.Size {
			return fmt.Errorf("backup entry %s is unexpected or has the wrong size", header.Name)
		}
		total += header.Size
		if total > maxBackupBytes {
			return errors.New("backup archive exceeds the verification limit")
		}
		hash := sha256.New()
		if copied, err := io.Copy(hash, tr); err != nil || copied != header.Size {
			return fmt.Errorf("read backup entry %s: %w", header.Name, err)
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
	tempName := temp.Name()
	defer os.Remove(tempName)
	// Set restrictive permissions immediately to protect backup manifest
	if err = temp.Chmod(0600); err != nil {
		temp.Close()
		return fmt.Errorf("secure backup manifest file permissions: %w", err)
	}
	// Write manifest data with proper permissions guaranteed
	if _, err = temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return fmt.Errorf("write backup manifest: %w", err)
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync backup manifest: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close backup manifest: %w", err)
	}
	if err := os.Rename(tempName, filepath.Join(root, "manifest.json")); err != nil {
		return err
	}
	if key != "" {
		signedData := append(append([]byte(nil), data...), '\n')
		if err := writeSyncedFile(filepath.Join(root, "manifest.sig"), []byte(backupManifestSignature(signedData, key)+"\n"), 0600); err != nil {
			return err
		}
	}
	return nil
}

func writeSyncedFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
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
	info, err := os.Stat(path)
	if err != nil {
		return BackupManifest{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return BackupManifest{}, errors.New("backup manifest is not a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return BackupManifest{}, err
	}
	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Version != 1 || safeUser(manifest.Site) == "" || manifest.Archive != "backup.tar.gz" || manifest.VerifiedAt.IsZero() || manifest.Bytes < 0 || len(manifest.ArchiveSHA256) != sha256.Size*2 {
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
	signature, err := os.ReadFile(signaturePath)
	if errors.Is(err, os.ErrNotExist) {
		if signingKey != "" || manifest.SignatureAlgorithm != "" {
			return errors.New("backup manifest signature is missing")
		}
		return nil
	}
	if err != nil {
		return err
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

func VerifySiteBackup(root string, signingKey ...string) (BackupManifest, error) {
	manifest, err := readBackupManifest(root)
	if err != nil {
		return BackupManifest{}, err
	}
	archivePath, err := safePath(root, manifest.Archive)
	if err != nil {
		return BackupManifest{}, err
	}
	if err := VerifyBackupArchive(archivePath, manifest); err != nil {
		return BackupManifest{}, err
	}
	manifestPath, err := safePath(root, "manifest.json")
	if err != nil {
		return BackupManifest{}, err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return BackupManifest{}, err
	}
	key := ""
	if len(signingKey) > 0 {
		key = signingKey[0]
	}
	if err := verifyBackupManifestSignature(root, data, manifest, key); err != nil {
		return BackupManifest{}, err
	}
	return manifest, nil
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
		manifest, err := VerifySiteBackup(path, signingKey...)
		if err != nil {
			// A damaged artifact must not hide every healthy backup from the
			// operator. Keep it visible in logs for quarantine/repair workflows.
			log.Printf("skip invalid backup manifest %s: %v", entry.Name(), err)
			continue
		}
		if site != "" && manifest.Site != site {
			continue
		}
		backups = append(backups, BackupResult{Site: manifest.Site, Path: path, ArchiveSHA256: manifest.ArchiveSHA256, Bytes: manifest.Bytes, Databases: manifest.Databases, VerifiedAt: manifest.VerifiedAt, Consistency: manifest.Consistency, ManifestSigned: manifest.SignatureAlgorithm != ""})
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].VerifiedAt.After(backups[j].VerifiedAt) })
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
		manifest, err := VerifySiteBackup(path, signingKey...)
		if err != nil {
			log.Printf("skip invalid backup manifest %s: %v", entry.Name(), err)
			continue
		}
		if manifest.Site != siteName {
			continue
		}
		backups = append(backups, BackupResult{Site: manifest.Site, Path: path, ArchiveSHA256: manifest.ArchiveSHA256, Bytes: manifest.Bytes, Databases: manifest.Databases, VerifiedAt: manifest.VerifiedAt, Consistency: manifest.Consistency, ManifestSigned: manifest.SignatureAlgorithm != ""})
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].VerifiedAt.After(backups[j].VerifiedAt) })
	if limit > 0 && len(backups) > limit {
		backups = backups[:limit]
	}
	return backups, nil
}
