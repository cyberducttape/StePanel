package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/importer"
	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

// durableArchiveImportRequest is the job payload for archive imports
type durableArchiveImportRequest struct {
	ArchiveURL       string `json:"archive_url"`
	ConfigPath       string `json:"config_path"`
	SiteName         string `json:"site_name"`
	DatabasePassword string `json:"database_password,omitempty"`
	AutoRestoreDB    bool   `json:"auto_restore_db,omitempty"`
}

// durableArchiveInspectionRequest is the job payload for archive inspections
type durableArchiveInspectionRequest struct {
	ArchiveURL string `json:"archive_url"`
	ConfigPath string `json:"config_path"`
}

// archiveInspectionRequest is the API request to inspect an archive
type archiveInspectionRequest struct {
	URL        string `json:"url"`         // URL to archive (S3, HTTP, etc.)
	ConfigPath string `json:"config_path"` // path to config file within archive
}

// archiveImportRequest is the API request to start importing from archive
type archiveImportRequest struct {
	URL              string `json:"url"`               // URL to archive
	ConfigPath       string `json:"config_path"`       // path to config file
	SiteName         string `json:"site_name"`         // name for new site
	DatabasePassword string `json:"database_password"` // required when auto_restore_db is true
	AutoRestoreDB    bool   `json:"auto_restore_db"`   // provision and restore an embedded SQL dump
}

// archiveInspectionStatus is the response showing inspection results
type archiveInspectionStatus struct {
	Status      string                      `json:"status"` // "pending", "analyzing", "done", "failed"
	Inspection  *importer.ArchiveInspection `json:"inspection,omitempty"`
	Error       string                      `json:"error,omitempty"`
	CompletedAt string                      `json:"completed_at,omitempty"`
}

func (a *App) inspectArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.IsAdministrator(r) || !a.Auth.CSRF(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}

	var req archiveInspectionRequest
	if err := decodeJSON(w, r, 4096, &req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.URL == "" || req.ConfigPath == "" {
		http.Error(w, "url and config_path are required", http.StatusBadRequest)
		return
	}

	// Enqueue inspection as a durable job (asynchronous)
	payload, err := json.Marshal(durableArchiveInspectionRequest{
		ArchiveURL: req.URL,
		ConfigPath: req.ConfigPath,
	})
	if err != nil {
		http.Error(w, "failed to marshal job payload: "+err.Error(), http.StatusInternalServerError)
		return
	}

	job, err := a.Jobs.Enqueue("archive.inspect", "admin", "archive-inspection", payload, 3)
	if err != nil {
		http.Error(w, "failed to enqueue inspection job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"job_id": job.ID,
		"status": "queued",
		"url":    req.URL,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}

func (a *App) inspectArchiveStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !a.Auth.IsAdministrator(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	jobID := r.URL.Query().Get("job_id")
	if jobID == "" {
		http.Error(w, "job_id required", http.StatusBadRequest)
		return
	}

	job, ok := a.Jobs.Get(jobID)
	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	response := map[string]interface{}{
		"job_id": job.ID,
		"status": job.State,
		"output": job.Output,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}

func (a *App) archiveImportStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.IsAdministrator(r) || !a.Auth.CSRF(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}

	var req archiveImportRequest
	if err := decodeJSON(w, r, 4096, &req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.URL == "" || req.ConfigPath == "" {
		http.Error(w, "url and config_path are required", http.StatusBadRequest)
		return
	}

	// Validate archive URL is HTTPS (prevents SSRF to unencrypted/local services)
	u, err := url.Parse(req.URL)
	if err != nil || u.Scheme != "https" {
		http.Error(w, "archive URL must be a valid HTTPS URL", http.StatusBadRequest)
		return
	}

	if req.SiteName == "" {
		http.Error(w, "site_name is required", http.StatusBadRequest)
		return
	}
	if req.DatabasePassword != "" && !req.AutoRestoreDB {
		http.Error(w, "database_password requires auto_restore_db=true", http.StatusUnprocessableEntity)
		return
	}
	if req.AutoRestoreDB && !validDatabasePassword(req.DatabasePassword) {
		http.Error(w, "automatic database restoration requires a valid database password", http.StatusUnprocessableEntity)
		return
	}

	// Validate site name
	if !validSiteName(req.SiteName) {
		http.Error(w, "invalid site name", http.StatusBadRequest)
		return
	}

	// Enqueue import job immediately without blocking on inspection.
	// The worker process will handle inspection, verification, provisioning,
	// restoration, and final verification.
	//
	// This keeps HTTP handlers responsive and prevents requests from tying up
	// while analyzing potentially multi-GB archives.
	payload, err := json.Marshal(durableArchiveImportRequest{
		ArchiveURL:       req.URL,
		ConfigPath:       req.ConfigPath,
		SiteName:         req.SiteName,
		DatabasePassword: req.DatabasePassword,
		AutoRestoreDB:    req.AutoRestoreDB,
	})
	if err != nil {
		http.Error(w, "failed to marshal job payload: "+err.Error(), http.StatusInternalServerError)
		return
	}

	job, err := a.Jobs.Enqueue("archive.import", "admin", req.SiteName, payload, 3)
	if err != nil {
		http.Error(w, "failed to enqueue import job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Return 202 Accepted to indicate the job is queued for processing
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	response := map[string]interface{}{
		"status":     "queued",
		"job_id":     job.ID,
		"site_name":  req.SiteName,
		"message":    "Archive import job queued. Inspection and restoration proceeding in worker process.",
		"status_url": "/api/jobs/" + job.ID,
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		// Response headers already sent; log error but cannot send error response
		return
	}
}

func (a *App) archiveImportStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !a.Auth.IsAdministrator(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	jobID := r.URL.Query().Get("job_id")
	if jobID == "" {
		http.Error(w, "job_id required", http.StatusBadRequest)
		return
	}

	job, ok := a.Jobs.Get(jobID)
	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	response := map[string]interface{}{
		"job_id": job.ID,
		"status": job.State,
		"output": job.Output,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}

// handleArchiveImportJob processes an archive.import durable job.
//
// Extraction goes into an isolated staging directory that is atomically
// renamed onto the canonical site path only once the whole archive is
// extracted and its config rewritten. A SiteTransaction journal wraps the
// activation so a crash between rename and commit is recoverable on boot via
// RecoverSiteTransactions. On any failure the staging tree is removed and the
// canonical path is left untouched.
func (a *App) handleArchiveImportJob(ctx context.Context, job *Job) error {
	var req durableArchiveImportRequest
	if err := json.Unmarshal(job.Payload, &req); err != nil {
		return fmt.Errorf("failed to parse job payload: %w", err)
	}
	if !validSiteName(req.SiteName) {
		return errors.New("invalid site name in archive import payload")
	}
	if a.Config.WebRoot == "" {
		return errors.New("archive import requires a configured web root")
	}

	actor := job.User
	if actor == "" {
		actor = "admin"
	}
	access, err := a.authorizeDurableSiteJob(req.SiteName, actor, false)
	if err != nil {
		return fmt.Errorf("archive import authorization: %w", err)
	}

	if ctx.Err() != nil || a.Jobs.CancellationRequested(job.ID) {
		return context.Canceled
	}

	operationCtx, releaseSite, lockErr := a.acquireSiteMutationLockContext(ctx, req.SiteName)
	if lockErr != nil {
		return fmt.Errorf("acquire site mutation lock: %w", lockErr)
	}
	defer releaseSite()

	// WebRoot is deployment authority, never job-payload data. Older queued
	// jobs may contain a legacy web_root field, but unknown JSON is ignored;
	// accepting it here would let a tampered payload redirect lifecycle
	// mutation to a different tree.
	canonical, err := safePath(a.Config.WebRoot, "sites", req.SiteName, "public")
	if err != nil {
		return fmt.Errorf("resolve canonical site: %w", err)
	}
	if _, err := os.Stat(canonical); err == nil {
		return fmt.Errorf("site %q already exists", req.SiteName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect canonical site: %w", err)
	}

	manager := a.siteManager
	if manager == nil {
		manager, err = siteauthority.NewDefaultManager(a.Config.WebRoot)
		if err != nil {
			return fmt.Errorf("initialize site manager for staging: %w", err)
		}
	}
	stagingDir, err := manager.CreateStaging(operationCtx, ".stepanel-import-")
	if err != nil {
		return fmt.Errorf("create import staging directory: %w", err)
	}

	activated := false
	defer func() {
		if !activated {
			if discardErr := manager.DiscardStaging(context.Background(), stagingDir); discardErr != nil {
				recordAudit(a.Config.AuditLog, actor, "archive.import.staging-cleanup-failed", req.SiteName, discardErr.Error())
			}
		}
	}()

	executor := importer.NewExecutor()
	if req.AutoRestoreDB {
		executor.WithDatabaseRestorer(a.restoreImportedDatabase)
	}
	progressUpdates := make([]map[string]interface{}, 0)

	result, err := executor.ExecuteImport(operationCtx, &importer.ArchiveImportRequest{
		URL:              req.ArchiveURL,
		ConfigPath:       req.ConfigPath,
		SiteName:         req.SiteName,
		DatabasePassword: req.DatabasePassword,
		AutoRestoreDB:    req.AutoRestoreDB,
	}, stagingDir, func(importJob *importer.ImportJob) {
		progressUpdates = append(progressUpdates, map[string]interface{}{
			"status":          importJob.Status,
			"progress":        importJob.Progress,
			"files_extracted": importJob.FilesExtracted,
			"bytes_extracted": importJob.BytesExtracted,
			"current_file":    importJob.CurrentFile,
			"message":         importJob.Message,
			"updated_at":      importJob.UpdatedAt,
		})
	})
	if err != nil {
		recordAudit(a.Config.AuditLog, actor, "archive.import.failed", req.SiteName, err.Error())
		return fmt.Errorf("archive extraction failed: %w", err)
	}
	var databaseCleanup importer.DatabaseCleanup
	databaseCommitted := false
	defer func() {
		if !databaseCommitted && databaseCleanup != nil {
			if cleanupErr := databaseCleanup(); cleanupErr != nil {
				recordAudit(a.Config.AuditLog, actor, "archive.import.database-rollback-failed", req.SiteName, cleanupErr.Error())
			}
		}
	}()
	if result != nil {
		databaseCleanup = result.Cleanup
	}

	txn, err := BeginSiteTransaction(a.Config.RecoveryRoot, canonical, "archive.import", access)
	if err != nil {
		return fmt.Errorf("begin archive import transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rbErr := txn.Rollback(); rbErr != nil {
				// The recovery journal has enough state to finish the rollback on next boot.
				recordAudit(a.Config.AuditLog, actor, "archive.import.rollback-failed", req.SiteName, rbErr.Error())
			}
		}
	}()

	if _, err := manager.ActivateStaged(operationCtx, req.SiteName, stagingDir); err != nil {
		return fmt.Errorf("activate imported site: %w", err)
	}
	activated = true

	if err := txn.Commit(); err != nil {
		// The tree is already at canonical; a subsequent boot-time recovery pass
		// treats the transaction as pending and rolls it back, which would
		// destroy the freshly-imported files. Surface the error so the operator
		// notices before that happens.
		return fmt.Errorf("commit archive import transaction: %w", err)
	}
	committed = true
	databaseCommitted = true
	databaseCleanup = nil

	if result != nil {
		// ExecuteImport does not restore a database. Preserve its explicit
		// incomplete status instead of advertising a site as ready when the
		// imported application still needs database work.
		result.CreatedAt = time.Now().UTC()
	}

	recordAudit(a.Config.AuditLog, actor, "archive.import.completed", req.SiteName, req.ArchiveURL)

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}
	job.Output = resultJSON

	return nil
}

// restoreImportedDatabase is the privileged adapter for generic archive
// imports. Provisioning and loading are separate helper operations, but the
// returned cleanup function keeps them part of the outer staged-import
// transaction until the site activation commits.
func (a *App) restoreImportedDatabase(ctx context.Context, dumpPath, database, user, password, site string) (importer.DatabaseCleanup, error) {
	if a.Config.DBCtl == "" {
		return nil, errors.New("managed database helper is unavailable")
	}
	if !validManagedDatabaseIdentifier(database, databaseNameLimit(a.Config)) || !validManagedDatabaseIdentifier(user, 32) || !validDatabasePassword(password) || safeUser(site) == "" {
		return nil, errors.New("invalid managed database restore parameters")
	}
	encoding := "utf8mb4"
	if a.Config.DBEngine == "postgresql" {
		encoding = "UTF8"
	}
	provisionCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	_, err := runBoundedCommandInput(provisionCtx, helperCommandContext(provisionCtx, a.Config, a.Config.DBCtl, "provision", database, user, site, encoding), strings.NewReader(password+"\n"))
	cancel()
	if err != nil {
		return nil, fmt.Errorf("provision managed database: %w", err)
	}
	cleanup := func() error {
		_, cleanupErr := runDatabaseHelperContext(ctx, a.Config, time.Minute, "", "drop-managed", database, user)
		return cleanupErr
	}
	dump, err := os.Open(dumpPath)
	if err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return nil, fmt.Errorf("open database dump: %w (cleanup also failed: %w)", err, cleanupErr)
		}
		return nil, fmt.Errorf("open database dump: %w", err)
	}
	restoreCtx, restoreCancel := context.WithTimeout(ctx, 15*time.Minute)
	_, err = runBoundedCommandInput(restoreCtx, helperCommandContext(restoreCtx, a.Config, a.Config.DBCtl, "restore-dump", database, site), dump)
	dump.Close()
	restoreCancel()
	if err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return nil, fmt.Errorf("restore managed database dump: %w (cleanup also failed: %w)", err, cleanupErr)
		}
		return nil, fmt.Errorf("restore managed database dump: %w", err)
	}
	verified, inventoryErr := managedDatabaseInventory(a.Config)
	if inventoryErr != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return nil, fmt.Errorf("verify restored managed database: %w (cleanup also failed: %w)", inventoryErr, cleanupErr)
		}
		return nil, fmt.Errorf("verify restored managed database: %w", inventoryErr)
	}
	found := false
	for _, item := range verified {
		if item.Name == database && item.Site == site {
			found = true
			break
		}
	}
	if !found {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return nil, fmt.Errorf("restored database %s was not present in managed inventory (cleanup also failed: %w)", database, cleanupErr)
		}
		return nil, fmt.Errorf("restored database %s was not present in managed inventory", database)
	}
	return cleanup, nil
}

// handleArchiveInspectionJob processes an archive inspection durable job
func (a *App) handleArchiveInspectionJob(ctx context.Context, job *Job) error {
	var req durableArchiveInspectionRequest
	if err := json.Unmarshal(job.Payload, &req); err != nil {
		return fmt.Errorf("failed to parse job payload: %w", err)
	}

	// Create analyzer and perform inspection
	analyzer := importer.NewAnalyzer()
	inspection, err := analyzer.InspectArchive(req.ArchiveURL, req.ConfigPath)

	// Always encode result (even if there was an error)
	result := map[string]interface{}{
		"inspection": inspection,
	}

	if err != nil {
		result["error"] = err.Error()
		result["success"] = false
	} else {
		result["success"] = true
	}

	// Store result in job output
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}
	job.Output = resultJSON

	return nil
}

// validSiteName checks if a site name is valid (matches standard site helpers: 1-32 chars)
func validSiteName(name string) bool {
	// Must match standard site identity constraints (same as stepanel-appctl)
	matched, _ := regexp.MatchString(`^[a-z0-9_-]{1,32}$`, name)
	return matched
}
