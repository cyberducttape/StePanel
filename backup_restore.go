package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cyberducttape/StePanel/internal/backup"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Type aliases for backward compatibility
type RestoreToStagingRequest = backup.RestoreToStagingRequest
type BackupRestoreResult = backup.BackupRestoreResult
type DurableBackupRestoreRequest = backup.DurableBackupRestoreRequest

// Lowercase alias for backward compatibility with existing code
type durableBackupRestoreRequest = backup.DurableBackupRestoreRequest

func (a *App) enqueueBackupRestoreJob(request durableBackupRestoreRequest) (Job, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return Job{}, err
	}
	job, _, err := a.Jobs.EnqueueIdempotent("backup.restore", request.Site, "", payload, 2)
	return job, err
}

func (a *App) handleBackupRestoreJob(ctx context.Context, item Job) ([]byte, error) {
	var request durableBackupRestoreRequest
	if err := json.Unmarshal(item.Payload, &request); err != nil {
		return nil, fmt.Errorf("decode backup restore job payload: %w", err)
	}
	if safeUser(request.Site) == "" || !validBackupName(request.Backup) || request.Actor == "" {
		return nil, errors.New("invalid durable backup restore payload")
	}
	access, err := a.authorizeDurableSiteJob(request.Site, request.Actor, false)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil || a.Jobs.CancellationRequested(item.ID) {
		return nil, context.Canceled
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(ctx, request.Site)
	if lockErr != nil {
		return nil, fmt.Errorf("acquire durable site lock: %w", lockErr)
	}
	defer releaseUnlock()
	var result BackupRestoreResult
	var restoreErr error
	switch request.Mode {
	case "files":
		result, restoreErr = backupRestoreFiles(operationCtx, a.Config, request.Backup, access)
	case "database":
		var safety BackupResult
		safety, restoreErr = CreateSiteBackup(a.Config, access, true)
		if restoreErr == nil {
			result, restoreErr = restoreManagedDatabase(operationCtx, a.Config, request.Backup, request.Site, request.Database)
			result.SafetyBackup = safety.Path
		}
	case "offsite-files":
		var root string
		var cleanup func()
		root, cleanup, restoreErr = downloadOffsiteBackup(a.Config, request.Site, request.Backup)
		if restoreErr == nil {
			defer cleanup()
			cfg := a.Config
			cfg.BackupRoot = root
			result, restoreErr = backupRestoreFiles(operationCtx, cfg, request.Backup, access)
		}
	case "offsite-database":
		var root string
		var cleanup func()
		root, cleanup, restoreErr = downloadOffsiteBackup(a.Config, request.Site, request.Backup)
		if restoreErr == nil {
			defer cleanup()
			var safety BackupResult
			safety, restoreErr = CreateSiteBackup(a.Config, access, true)
			if restoreErr == nil {
				cfg := a.Config
				cfg.BackupRoot = root
				result, restoreErr = restoreManagedDatabase(operationCtx, cfg, request.Backup, request.Site, request.Database)
				result.SafetyBackup = safety.Path
			}
		}
	default:
		return nil, errors.New("unsupported backup restore mode")
	}
	if restoreErr != nil {
		recordAudit(a.Config.AuditLog, request.Actor, "backup."+request.Mode+".failed", request.Site, restoreErr.Error())
		return nil, restoreErr
	}
	recordAudit(a.Config.AuditLog, request.Actor, "backup."+request.Mode+".completed", request.Site, request.Backup)
	output, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return output, nil
}

// backupVerify performs the same archive and manifest checks used before a
// restore, without extracting or changing host state.
func (a *App) backupVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input struct {
		Backup string `json:"backup"`
		Site   string `json:"site"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(input.Site)
	input.Backup = strings.TrimSpace(input.Backup)
	if input.Site == "" || !validBackupName(input.Backup) {
		http.Error(w, "invalid backup", http.StatusUnprocessableEntity)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, input.Site, "invalid backup", http.StatusUnprocessableEntity); !ok {
		return
	}
	path, err := safePath(a.Config.BackupRoot, input.Backup)
	if err != nil {
		http.Error(w, "invalid backup", http.StatusUnprocessableEntity)
		return
	}
	manifest, err := VerifySiteBackup(path, a.Config.BackupSigningKey)
	if err != nil {
		http.Error(w, "backup verification failed", http.StatusUnprocessableEntity)
		return
	}
	if manifest.Site != input.Site {
		http.Error(w, "backup does not belong to site", http.StatusForbidden)
		return
	}
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "backup.verify", input.Site, input.Backup)
	writeJSON(w, http.StatusOK, map[string]any{"verified": true, "backup": input.Backup, "site": manifest.Site, "consistency": manifest.Consistency, "archive_verified": manifest.ArchiveVerified, "database_dump_verified": manifest.DatabaseDumpVerified, "application_quiesced": manifest.ApplicationQuiesced, "filesystem_snapshot": manifest.FilesystemSnapshot, "manifest_signed": manifest.SignatureAlgorithm != ""})
}

func (a *App) backupRestoreToStaging(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var input RestoreToStagingRequest
	if e := decodeJSON(w, r, 4096, &input); e != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input.Site = safeUser(input.Site)
	input.Backup = strings.TrimSpace(input.Backup)
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	backup, err := safePath(a.Config.BackupRoot, input.Backup)
	if !validBackupName(input.Backup) || err != nil {
		http.Error(w, "invalid backup", 422)
		return
	}
	a.backupRestoreToStagingPath(w, r, input, backup)
}

func (a *App) backupRestoreOffsiteToStaging(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var input RestoreToStagingRequest
	if e := decodeJSON(w, r, 4096, &input); e != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input.SourceSite = safeUser(input.SourceSite)
	input.Site = safeUser(input.Site)
	input.Backup = strings.TrimSpace(input.Backup)
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	if input.SourceSite == "" || !validBackupName(input.Backup) {
		http.Error(w, "invalid or inaccessible source site", http.StatusForbidden)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, input.SourceSite, "invalid or inaccessible source site", http.StatusForbidden); !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "backup:restore") {
		http.Error(w, "API token lacks the backup:restore scope", http.StatusForbidden)
		return
	}
	root, cleanup, err := downloadOffsiteBackup(a.Config, input.SourceSite, input.Backup)
	if err != nil {
		http.Error(w, "could not download offsite backup: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer cleanup()
	a.backupRestoreToStagingPath(w, r, input, root)
}

func (a *App) backupRestoreToStagingPath(w http.ResponseWriter, r *http.Request, input RestoreToStagingRequest, backup string) {
	if input.Site == "" || input.Backup == "." || input.Backup == "" || !domainPattern.MatchString(input.Domain) {
		http.Error(w, "invalid restore destination", 422)
		return
	}
	access, ok := a.requireSiteAccess(w, r, input.Site, "invalid restore destination", 422)
	if !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "backup:restore") {
		http.Error(w, "API token lacks the backup:restore scope", http.StatusForbidden)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "site restore is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	manifest, e := VerifySiteBackup(backup, a.Config.BackupSigningKey)
	if e != nil {
		http.Error(w, "backup verification failed", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, manifest.Site, "backup source site is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	// Validate database restore parameters (if provided)
	input.Database = strings.ToLower(strings.TrimSpace(input.Database))
	input.TargetDatabase = strings.ToLower(strings.TrimSpace(input.TargetDatabase))
	input.TargetUser = strings.ToLower(strings.TrimSpace(input.TargetUser))

	dbValidationErrors := ValidateRestoreDatabaseInput(a.Config, RestoreDatabaseInput{
		Database:       input.Database,
		TargetDatabase: input.TargetDatabase,
		TargetUser:     input.TargetUser,
		TargetPassword: input.TargetPassword,
	}, manifest)
	if len(dbValidationErrors) > 0 {
		http.Error(w, strings.Join(dbValidationErrors, "; "), http.StatusUnprocessableEntity)
		return
	}

	// Determine if database restoration is requested
	hasDatabase := input.Database != "" && input.TargetDatabase != "" && input.TargetUser != "" && input.TargetPassword != ""
	dest, e := safePath(a.Config.WebRoot, "sites", input.Site, "public")
	if e != nil {
		http.Error(w, "invalid destination", 422)
		return
	}
	if _, e = os.Stat(dest); e == nil {
		http.Error(w, "staging destination already exists", 409)
		return
	}
	stage, e := os.MkdirTemp(a.Config.ImportRoot, "backup-restore-")
	if e != nil {
		http.Error(w, "could not prepare restore staging", 500)
		return
	}
	defer os.RemoveAll(stage)
	archivePath, archiveErr := safePath(backup, manifest.Archive)
	if archiveErr != nil {
		http.Error(w, "invalid verified backup path", 422)
		return
	}
	if e = extractArchiveContext(operationCtx, archivePath, stage); e != nil {
		http.Error(w, "could not extract verified backup", 502)
		return
	}
	source, e := safePath(stage, "site", "public")
	if e != nil {
		http.Error(w, "invalid backup layout", 422)
		return
	}
	if info, statErr := os.Stat(source); statErr != nil || !info.IsDir() {
		http.Error(w, "backup has no site files", 422)
		return
	}
	if e = siteHelperContext(operationCtx, a.Config, "prepare", input.Site); e != nil {
		http.Error(w, "could not prepare isolated staging site", 502)
		return
	}
	marker, markerErr := safePath(a.Config.WebRoot, "sites", input.Site, ".stepanel-staging-noindex")
	if markerErr != nil {
		http.Error(w, "invalid staging marker path", 422)
		return
	}
	if e = writeAtomic(marker, []byte("managed restore staging noindex\n"), 0600); e != nil {
		http.Error(w, "could not apply restore indexing protection", 503)
		return
	}
	txn, e := BeginSiteTransaction(a.Config.RecoveryRoot, dest, "backup.restore-to-staging", access)
	if e != nil {
		http.Error(w, "could not journal restore", 503)
		return
	}
	committed := false
	createdDatabase := false
	defer func() {
		if !committed {
			_ = txn.Rollback()
			if createdDatabase {
				if _, cleanupErr := runDatabaseHelperContext(operationCtx, a.Config, time.Minute, "", "drop-managed", input.TargetDatabase, input.TargetUser); cleanupErr != nil {
					log.Printf("staging database cleanup failed for %s: %v", input.TargetDatabase, cleanupErr)
				}
			}
		}
	}()
	if e = copyTreeContext(operationCtx, source, dest); e != nil {
		http.Error(w, "could not restore site files", 502)
		return
	}
	if e = siteHelperContext(operationCtx, a.Config, "seal", input.Site); e != nil {
		http.Error(w, "could not seal restored site", 502)
		return
	}
	if hasDatabase {
		var databaseCreated bool
		databaseCreated, e = restoreDatabaseIntoStagingContext(operationCtx, a.Config, stage, input)
		createdDatabase = databaseCreated
		if e != nil {
			http.Error(w, "could not restore staging database: "+e.Error(), 502)
			return
		}
	}
	if e = runHelperCommandWithTimeout(operationCtx, a.Config, helperConfigMutationTimeout, a.Config.VHostCtl, "apply", input.Site, input.Domain); e != nil {
		http.Error(w, "could not activate restored staging route", 502)
		return
	}
	if e = txn.Commit(); e != nil {
		http.Error(w, "could not commit restore", 503)
		return
	}
	committed = true
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "backup.restore-to-staging", input.Site, input.Backup)
	writeJSON(w, 202, map[string]any{"site": input.Site, "domain": input.Domain, "backup": input.Backup, "source_site": manifest.Site, "files_restored": true, "databases_restored": hasDatabase, "database": input.TargetDatabase, "restore_mode": "staging", "consistency": manifest.Consistency, "created_at": time.Now().UTC()})
}

func restoreDatabaseIntoStaging(cfg Config, stage string, input RestoreToStagingRequest) (bool, error) {
	return restoreDatabaseIntoStagingContext(context.Background(), cfg, stage, input)
}

func restoreDatabaseIntoStagingContext(ctx context.Context, cfg Config, stage string, input RestoreToStagingRequest) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !validManagedDatabaseIdentifier(input.Database, databaseNameLimit(cfg)) {
		return false, errors.New("invalid staging database name")
	}
	dump, err := safePath(stage, "databases", input.Database+".sql")
	if err != nil {
		return false, err
	}
	info, err := os.Stat(dump)
	if err != nil || !info.Mode().IsRegular() {
		return false, errors.New("selected database dump is unavailable")
	}
	encoding := "utf8mb4"
	if cfg.DBEngine == "postgresql" {
		encoding = "UTF8"
	}
	if _, err := runDatabaseHelperContext(ctx, cfg, time.Minute, input.TargetPassword, "provision", input.TargetDatabase, input.TargetUser, input.Site, encoding); err != nil {
		return false, fmt.Errorf("provision staging database: %w", err)
	}
	file, err := os.Open(dump)
	if err != nil {
		return true, err
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(ctx, helperBackupRestoreTimeout)
	defer cancel()
	cmd := helperCommandContext(ctx, cfg, cfg.DBCtl, "restore-dump", input.TargetDatabase, input.Site)
	cmd.Stdin = file
	output, err := runBoundedCommand(ctx, cmd)
	if err != nil {
		return true, fmt.Errorf("import staging database: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return true, nil
}

// backupRestoreFiles replaces only the managed site files. It deliberately
// leaves databases untouched and uses the normal site recovery journal so an
// interrupted extraction can be resumed or rolled back by the operator.
func backupRestoreFiles(ctx context.Context, cfg Config, backupName string, site SiteCapability) (BackupRestoreResult, error) {
	if err := ctx.Err(); err != nil {
		return BackupRestoreResult{}, err
	}
	siteName := site.Site()
	if !validBackupName(backupName) {
		return BackupRestoreResult{}, errors.New("invalid backup path")
	}
	backup, err := safePath(cfg.BackupRoot, backupName)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	manifest, err := VerifySiteBackup(backup, cfg.BackupSigningKey)
	if err != nil {
		return BackupRestoreResult{}, fmt.Errorf("verify backup: %w", err)
	}
	if manifest.Site != siteName {
		return BackupRestoreResult{}, errors.New("backup does not belong to destination site")
	}
	if err := os.MkdirAll(cfg.ImportRoot, 0700); err != nil {
		return BackupRestoreResult{}, fmt.Errorf("create restore staging root: %w", err)
	}
	stage, err := os.MkdirTemp(cfg.ImportRoot, "backup-files-restore-")
	if err != nil {
		return BackupRestoreResult{}, err
	}
	defer os.RemoveAll(stage)
	archivePath, err := safePath(backup, manifest.Archive)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	if err := extractArchiveContext(ctx, archivePath, stage); err != nil {
		return BackupRestoreResult{}, fmt.Errorf("extract verified backup: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return BackupRestoreResult{}, err
	}
	source, err := safePath(stage, "site", "public")
	if err != nil {
		return BackupRestoreResult{}, fmt.Errorf("invalid backup layout: %w", err)
	}
	if info, err := os.Stat(source); err != nil || !info.IsDir() {
		return BackupRestoreResult{}, errors.New("backup has no site files")
	}
	dest, err := safePath(cfg.WebRoot, "sites", siteName, "public")
	if err != nil {
		return BackupRestoreResult{}, err
	}
	txn, err := BeginSiteTransaction(cfg.RecoveryRoot, dest, "backup.restore-files", site)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = txn.Rollback()
		}
	}()
	if err := siteHelperContext(ctx, cfg, "prepare", siteName); err != nil {
		return BackupRestoreResult{}, fmt.Errorf("prepare site: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return BackupRestoreResult{}, err
	}
	if err := copyTreeContext(ctx, source, dest); err != nil {
		return BackupRestoreResult{}, fmt.Errorf("restore site files: %w", err)
	}
	if err := siteHelperContext(ctx, cfg, "seal", siteName); err != nil {
		return BackupRestoreResult{}, fmt.Errorf("seal site: %w", err)
	}
	if err := txn.Commit(); err != nil {
		return BackupRestoreResult{}, fmt.Errorf("commit restore journal: %w", err)
	}
	ok = true
	return BackupRestoreResult{Site: siteName, Backup: filepath.Base(backup), Mode: "files-only", FilesRestored: true, DatabasePreserved: true, Consistency: manifest.Consistency, CompletedAt: time.Now().UTC()}, nil
}

func (a *App) backupRestoreFilesHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}
	var input struct {
		Backup  string `json:"backup"`
		Site    string `json:"site"`
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(input.Site)
	input.Backup = strings.TrimSpace(input.Backup)
	if input.Site == "" || !validBackupName(input.Backup) || input.Confirm != "RESTORE_FILES" {
		http.Error(w, "site, backup, and confirm=RESTORE_FILES are required", http.StatusUnprocessableEntity)
		return
	}
	if a.Jobs == nil {
		http.Error(w, "job system unavailable", http.StatusServiceUnavailable)
		return
	}
	backup, err := safePath(a.Config.BackupRoot, input.Backup)
	if err != nil {
		http.Error(w, "invalid backup", http.StatusUnprocessableEntity)
		return
	}
	if _, err := VerifySiteBackup(backup, a.Config.BackupSigningKey); err != nil {
		http.Error(w, "backup verification failed", http.StatusUnprocessableEntity)
		return
	}
	job, err := a.enqueueBackupRestoreJob(durableBackupRestoreRequest{Mode: "files", Site: input.Site, Backup: input.Backup, Actor: a.Auth.UsernameForRequest(r)})
	if err != nil {
		http.Error(w, "could not persist restore job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID, "mode": "files-only"})
}

func backupContainsDatabase(manifest BackupManifest, database string) bool {
	for _, name := range manifest.Databases {
		if name == database {
			return true
		}
	}
	return false
}

func restoreManagedDatabase(ctx context.Context, cfg Config, backupName, site, database string) (BackupRestoreResult, error) {
	if err := ctx.Err(); err != nil {
		return BackupRestoreResult{}, err
	}
	if !validBackupName(backupName) || !validManagedDatabaseIdentifier(database, databaseNameLimit(cfg)) {
		return BackupRestoreResult{}, errors.New("invalid backup path")
	}
	backup, err := safePath(cfg.BackupRoot, backupName)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	manifest, err := VerifySiteBackup(backup, cfg.BackupSigningKey)
	if err != nil {
		return BackupRestoreResult{}, fmt.Errorf("verify backup: %w", err)
	}
	if !backupContainsDatabase(manifest, database) {
		return BackupRestoreResult{}, errors.New("backup does not contain the selected database")
	}
	if cfg.DBCtl == "" {
		return BackupRestoreResult{}, errors.New("managed database helper is not configured")
	}
	if err := os.MkdirAll(cfg.ImportRoot, 0700); err != nil {
		return BackupRestoreResult{}, fmt.Errorf("create restore staging root: %w", err)
	}
	stage, err := os.MkdirTemp(cfg.ImportRoot, "backup-database-restore-")
	if err != nil {
		return BackupRestoreResult{}, err
	}
	defer os.RemoveAll(stage)
	archivePath, err := safePath(backup, manifest.Archive)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	if err := extractArchiveContext(ctx, archivePath, stage); err != nil {
		return BackupRestoreResult{}, fmt.Errorf("extract verified backup: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return BackupRestoreResult{}, err
	}
	dump, err := safePath(stage, "databases", database+".sql")
	if err != nil {
		return BackupRestoreResult{}, err
	}
	info, err := os.Stat(dump)
	if err != nil || !info.Mode().IsRegular() {
		return BackupRestoreResult{}, errors.New("selected database dump is unavailable")
	}
	input, err := os.Open(dump)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	defer input.Close()
	ctx, cancel := context.WithTimeout(ctx, helperBackupRestoreTimeout)
	defer cancel()
	cmd := helperCommandContext(ctx, cfg, cfg.DBCtl, "restore-dump", database, site)
	cmd.Stdin = input
	output, err := runBoundedCommand(ctx, cmd)
	if err != nil {
		return BackupRestoreResult{}, fmt.Errorf("restore database %s: %w: %s", database, err, strings.TrimSpace(string(output)))
	}
	return BackupRestoreResult{Site: site, Backup: filepath.Base(backup), Mode: "database-only", Database: database, DatabaseRestored: true, DatabasePreserved: false, Consistency: manifest.Consistency, SchemaRollback: "manual: restore the safety backup or apply a forward migration", CompletedAt: time.Now().UTC()}, nil
}

func (a *App) backupRestoreDatabaseHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}
	var input struct {
		Backup   string `json:"backup"`
		Site     string `json:"site"`
		Database string `json:"database"`
		Confirm  string `json:"confirm"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(input.Site)
	input.Backup = strings.TrimSpace(input.Backup)
	if input.Site == "" || !validBackupName(input.Backup) || !validManagedDatabaseIdentifier(input.Database, 64) || input.Confirm != "RESTORE_DATABASE" {
		http.Error(w, "site, backup, database, and confirm=RESTORE_DATABASE are required", http.StatusUnprocessableEntity)
		return
	}
	if a.Jobs == nil || a.Config.DBCtl == "" {
		http.Error(w, "managed database restore is unavailable", http.StatusServiceUnavailable)
		return
	}
	backup, err := safePath(a.Config.BackupRoot, input.Backup)
	if err != nil {
		http.Error(w, "invalid backup", http.StatusUnprocessableEntity)
		return
	}
	manifest, err := VerifySiteBackup(backup, a.Config.BackupSigningKey)
	if err != nil || manifest.Site != input.Site || !backupContainsDatabase(manifest, input.Database) {
		http.Error(w, "verified backup does not contain the selected site database", http.StatusUnprocessableEntity)
		return
	}
	job, err := a.enqueueBackupRestoreJob(durableBackupRestoreRequest{Mode: "database", Site: input.Site, Backup: input.Backup, Database: input.Database, Actor: a.Auth.UsernameForRequest(r)})
	if err != nil {
		http.Error(w, "could not persist restore job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID, "mode": "database-only", "schema_rollback": "manual"})
}

func (a *App) backupRestoreOffsiteFilesHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}
	var input struct {
		Backup  string `json:"backup"`
		Site    string `json:"site"`
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(input.Site)
	input.Backup = strings.TrimSpace(input.Backup)
	if input.Site == "" || !validBackupName(input.Backup) || input.Confirm != "RESTORE_OFFSITE_FILES" {
		http.Error(w, "site, backup, and confirm=RESTORE_OFFSITE_FILES are required", http.StatusUnprocessableEntity)
		return
	}
	if a.Jobs == nil || a.Config.OffsiteTarget == "" {
		http.Error(w, "offsite backup restore is unavailable", http.StatusServiceUnavailable)
		return
	}
	job, err := a.enqueueBackupRestoreJob(durableBackupRestoreRequest{Mode: "offsite-files", Site: input.Site, Backup: input.Backup, Actor: a.Auth.UsernameForRequest(r)})
	if err != nil {
		http.Error(w, "could not persist restore job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID, "mode": "offsite-files-only"})
}

func (a *App) backupRestoreOffsiteDatabaseHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}
	var input struct {
		Backup   string `json:"backup"`
		Site     string `json:"site"`
		Database string `json:"database"`
		Confirm  string `json:"confirm"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(input.Site)
	input.Backup = strings.TrimSpace(input.Backup)
	if input.Site == "" || !validBackupName(input.Backup) || !validManagedDatabaseIdentifier(input.Database, 64) || input.Confirm != "RESTORE_OFFSITE_DATABASE" {
		http.Error(w, "site, backup, database, and confirm=RESTORE_OFFSITE_DATABASE are required", http.StatusUnprocessableEntity)
		return
	}
	if a.Jobs == nil || a.Config.OffsiteTarget == "" || a.Config.DBCtl == "" {
		http.Error(w, "offsite database restore is unavailable", http.StatusServiceUnavailable)
		return
	}
	job, err := a.enqueueBackupRestoreJob(durableBackupRestoreRequest{Mode: "offsite-database", Site: input.Site, Backup: input.Backup, Database: input.Database, Actor: a.Auth.UsernameForRequest(r)})
	if err != nil {
		http.Error(w, "could not persist restore job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID, "mode": "offsite-database-only", "schema_rollback": "manual"})
}
