package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/archivesafe"
	"github.com/cyberducttape/StePanel/internal/importer"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// durableArchiveImportRequest is the job payload for archive imports
type durableArchiveImportRequest struct {
	ArchiveURL       string `json:"archive_url"`
	ConfigPath       string `json:"config_path"`
	SiteName         string `json:"site_name"`
	DatabasePassword string `json:"database_password,omitempty"`
	AutoRestoreDB    bool   `json:"auto_restore_db,omitempty"`
	// Account, Kind and Domain place the imported site in a hosting account
	// (ADR-0007), as for site creation.
	Account string `json:"account,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Domain  string `json:"domain,omitempty"`
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
	// Account names the hosting account the site is placed in (the site name
	// when empty); Kind and Domain choose main, addon or subdomain.
	Account string `json:"account"`
	Kind    string `json:"kind"`
	Domain  string `json:"domain"`
}

// archiveInspectionStatus is the response showing inspection results
type archiveInspectionStatus struct {
	Status      string                      `json:"status"` // "pending", "analyzing", "done", "failed"
	Inspection  *importer.ArchiveInspection `json:"inspection,omitempty"`
	Error       string                      `json:"error,omitempty"`
	CompletedAt string                      `json:"completed_at,omitempty"`
}

type archiveInspector interface {
	InspectArchive(ctx context.Context, url, configPath string) (*importer.ArchiveInspection, error)
}

var newArchiveAnalyzer = func(spool importer.ArchiveSpool) archiveInspector {
	return importer.NewAnalyzer().WithSpool(spool)
}

// archiveSpoolPrefix names ZIP spool files in the import root; startup
// cleanup removes ones a crash left behind.
const archiveSpoolPrefix = ".stepanel-spool-"

// archiveSpool stores remote inspections and ZIP imports in the
// capacity-managed import root. Its progressive reservation is acquired before
// the first byte, converted to actual usage as the file grows, and held until
// the archive is closed, so concurrent processes cannot spend the same space.
func (a *App) archiveSpool(workflow string) importer.ArchiveSpool {
	return archivesafe.DirSpool{
		Dir: a.Config.ImportRoot,
		Reserve: func(limit int64) (func(int64) error, func(), error) {
			if limit <= 0 {
				return nil, nil, errors.New("archive spool capacity limit must be positive")
			}
			demandsFor := func(bytes uint64) []capacityDemand {
				return []capacityDemand{{Path: a.Config.ImportRoot, Bytes: bytes}}
			}
			reservation, err := a.capacityLedger().reserveProgressive(a.Config, workflow, a.Config.ImportRoot, uint64(limit), demandsFor)
			if err != nil {
				return nil, nil, err
			}
			return reservation.consume, reservation.release, nil
		},
	}
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

	job, ok, err := a.Jobs.GetWithError(jobID)
	if err != nil {
		http.Error(w, "archive inspection status is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
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
	if a.Jobs == nil {
		http.Error(w, "archive imports require the durable job store", http.StatusServiceUnavailable)
		return
	}
	// Check the placement now so a bad account, kind or domain is refused
	// before a job is queued; the job resolves it again under the site lock.
	if db := sitePlacementDB.Load(); db != nil {
		if _, err := resolveSitePlacement(db, req.SiteName, importPlacementRequest(req.SiteName, req.Account, req.Kind, req.Domain)); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
	}
	if req.AutoRestoreDB && !a.Jobs.PayloadEncryptionEnabled() {
		http.Error(w, "archive database restore requires STEPANEL_ACCOUNT_KEY for encrypted durable job payloads", http.StatusServiceUnavailable)
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
		Account:          req.Account,
		Kind:             req.Kind,
		Domain:           req.Domain,
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

	job, ok, err := a.Jobs.GetWithError(jobID)
	if err != nil {
		http.Error(w, "archive import status is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
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
func (a *App) handleArchiveImportJob(ctx context.Context, job *Job) ([]byte, error) {
	var req durableArchiveImportRequest
	if err := json.Unmarshal(job.Payload, &req); err != nil {
		return nil, fmt.Errorf("failed to parse job payload: %w", err)
	}
	if !validSiteName(req.SiteName) {
		return nil, errors.New("invalid site name in archive import payload")
	}
	if a.Config.WebRoot == "" {
		return nil, errors.New("archive import requires a configured web root")
	}

	actor := job.User
	if actor == "" {
		actor = "admin"
	}
	access, err := a.authorizeDurableSiteJob(req.SiteName, actor, false)
	if err != nil {
		return nil, fmt.Errorf("archive import authorization: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := a.Jobs.CheckNotCancelled(job.ID); err != nil {
		return nil, err
	}

	operationCtx, releaseSite, lockErr := a.acquireSiteMutationLockContext(ctx, req.SiteName)
	if lockErr != nil {
		return nil, fmt.Errorf("acquire site mutation lock: %w", lockErr)
	}
	defer releaseSite()

	// A new site goes into a hosting account (ADR-0007). A site that already
	// exists in either layout is refused below, never overwritten.
	committed := false
	if db := sitePlacementDB.Load(); db != nil {
		if _, legacyErr := existingManagedSiteRoot(a.Config.WebRoot, req.SiteName); legacyErr == nil {
			return nil, fmt.Errorf("site %q already exists", req.SiteName)
		}
		placement, err := resolveSitePlacement(db, req.SiteName, importPlacementRequest(req.SiteName, req.Account, req.Kind, req.Domain))
		if err != nil {
			return nil, fmt.Errorf("resolve hosting-account placement: %w", err)
		}
		if existing, err := sitelayout.Get(db, req.SiteName); err == nil && existing != placement {
			return nil, fmt.Errorf("site %q already exists in hosting account %s", req.SiteName, existing.Account)
		}
		placedByJob, err := placeSiteInAccount(operationCtx, a.Config, db, placement)
		if err != nil {
			return nil, fmt.Errorf("place imported site in hosting account %s: %w", placement.Account, err)
		}
		if placedByJob {
			defer func() {
				if committed {
					return
				}
				if err := unplaceSite(context.WithoutCancel(operationCtx), a.Config, db, req.SiteName); err != nil {
					TelemetryAudit(a.Config.AuditLog, actor, "archive.import.unplace-failed", req.SiteName, err.Error())
				}
			}()
		}
	}

	// WebRoot is deployment authority, never job-payload data. Older queued
	// jobs may contain a legacy web_root field, but unknown JSON is ignored;
	// accepting it here would let a tampered payload redirect lifecycle
	// mutation to a different tree.
	canonical, err := siteDocRoot(a.Config, req.SiteName)
	if err != nil {
		return nil, fmt.Errorf("resolve canonical site: %w", err)
	}
	if _, err := os.Stat(canonical); err == nil {
		return nil, fmt.Errorf("site %q already exists", req.SiteName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect canonical site: %w", err)
	}

	manager := a.siteManager
	if manager == nil {
		manager, err = newSiteManagerForConfig(a.Config)
		if err != nil {
			return nil, fmt.Errorf("initialize site manager for staging: %w", err)
		}
	}
	stagingDir, stagingOwner, err := manager.CreateSiteStagingLease(operationCtx, req.SiteName, ".stepanel-import-")
	if err != nil {
		return nil, fmt.Errorf("create import staging directory: %w", err)
	}
	defer func() {
		if err := manager.ReleaseStagingLease(stagingDir, stagingOwner); err != nil {
			log.Printf("release import staging lease for %s: %v", req.SiteName, err)
		}
	}()

	activated := false
	defer func() {
		if !activated {
			if discardErr := manager.DiscardStaging(context.Background(), stagingDir); discardErr != nil {
				TelemetryAudit(a.Config.AuditLog, actor, "archive.import.staging-cleanup-failed", req.SiteName, discardErr.Error())
			}
		}
	}()

	executor := importer.NewExecutor().WithSpool(a.archiveSpool("archive import"))
	if req.AutoRestoreDB {
		executor.WithDatabaseRestorer(a.restoreImportedDatabase)
	}
	result, err := executor.ExecuteImport(operationCtx, &importer.ArchiveImportRequest{
		URL:              req.ArchiveURL,
		ConfigPath:       req.ConfigPath,
		SiteName:         req.SiteName,
		DatabasePassword: req.DatabasePassword,
		AutoRestoreDB:    req.AutoRestoreDB,
	}, stagingDir, func(importJob *importer.ImportJob) {
		// Persist progress on the durable job instead of accumulating an
		// unbounded in-memory history that is never read by the UI.
		if err := a.Jobs.UpdateClaim(job.ID, job.LeaseOwner, importJob.Progress); err != nil {
			TelemetryAudit(a.Config.AuditLog, actor, "archive.import.progress-persist-failed", req.SiteName, err.Error())
		}
	})
	if err != nil {
		TelemetryAudit(a.Config.AuditLog, actor, "archive.import.failed", req.SiteName, err.Error())
		return nil, fmt.Errorf("archive extraction failed: %w", err)
	}
	var databaseCleanup importer.DatabaseCleanup
	databaseCommitted := false
	defer func() {
		if !databaseCommitted && databaseCleanup != nil {
			if cleanupErr := databaseCleanup(); cleanupErr != nil {
				TelemetryAudit(a.Config.AuditLog, actor, "archive.import.database-rollback-failed", req.SiteName, cleanupErr.Error())
			}
		}
	}()
	if result != nil {
		databaseCleanup = result.Cleanup
	}

	txn, err := beginManagedSiteTransaction(operationCtx, a.Config, canonical, "archive.import", access)
	if err != nil {
		return nil, fmt.Errorf("begin archive import transaction: %w", err)
	}
	defer func() {
		if !committed {
			if rbErr := txn.Rollback(); rbErr != nil {
				// The recovery journal has enough state to finish the rollback on next boot.
				TelemetryAudit(a.Config.AuditLog, actor, "archive.import.rollback-failed", req.SiteName, rbErr.Error())
			}
		}
	}()

	// Give the site its isolated account and PHP pool before publishing it,
	// and seal ownership and ACLs onto the published tree, as cpMove restores
	// do; otherwise the imported files belong to the panel and nothing serves
	// them as the site.
	if err := siteHelperContext(operationCtx, a.Config, "prepare", req.SiteName); err != nil {
		return nil, fmt.Errorf("prepare imported site: %w", err)
	}
	if _, err := manager.ActivateStaged(operationCtx, req.SiteName, stagingDir); err != nil {
		return nil, fmt.Errorf("activate imported site: %w", err)
	}
	activated = true
	if err := siteHelperContext(operationCtx, a.Config, "seal", req.SiteName); err != nil {
		return nil, fmt.Errorf("seal imported site: %w", err)
	}

	// Invalidate metadata caches after site activation to ensure fresh data
	if a.MetadataCache != nil {
		a.MetadataCache.InvalidateSite(req.SiteName)
	}

	if err := txn.Commit(); err != nil {
		// The tree is already at canonical; a subsequent boot-time recovery pass
		// treats the transaction as pending and rolls it back, which would
		// destroy the freshly-imported files. Surface the error so the operator
		// notices before that happens.
		return nil, fmt.Errorf("commit archive import transaction: %w", err)
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

	TelemetryAudit(a.Config.AuditLog, actor, "archive.import.completed", req.SiteName, req.ArchiveURL)

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	return resultJSON, nil
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
	_, err := runDatabaseHelperContext(provisionCtx, a.Config, 2*time.Minute, password, "provision", database, user, site, encoding)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("provision managed database: %w", err)
	}
	cleanup := func() error {
		_, cleanupErr := runDatabaseHelperContext(ctx, a.Config, time.Minute, "", "drop-managed", database, user)
		return cleanupErr
	}
	if _, err := os.Stat(dumpPath); err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return nil, fmt.Errorf("open database dump: %w (cleanup also failed: %w)", err, cleanupErr)
		}
		return nil, fmt.Errorf("open database dump: %w", err)
	}
	restoreCtx, restoreCancel := context.WithTimeout(ctx, 15*time.Minute)
	err = runDatabaseRestoreFromPath(restoreCtx, a.Config, "restore-dump", site, database, "restore", "", dumpPath, a.capacityLedger())
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
func (a *App) handleArchiveInspectionJob(ctx context.Context, job *Job) ([]byte, error) {
	var req durableArchiveInspectionRequest
	if err := json.Unmarshal(job.Payload, &req); err != nil {
		return nil, fmt.Errorf("failed to parse job payload: %w", err)
	}

	// Create analyzer and perform inspection
	analyzer := newArchiveAnalyzer(a.archiveSpool("archive inspection"))
	inspection, inspectionErr := analyzer.InspectArchive(ctx, req.ArchiveURL, req.ConfigPath)

	// Always encode result (even if there was an error)
	result := map[string]interface{}{
		"inspection": inspection,
	}

	if inspectionErr != nil {
		result["error"] = inspectionErr.Error()
		result["success"] = false
	} else {
		result["success"] = true
	}

	// Return the result so the durable worker can persist it on completion.
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}
	if inspectionErr != nil {
		return resultJSON, fmt.Errorf("archive inspection failed: %w", inspectionErr)
	}
	return resultJSON, nil
}

// validSiteName checks if a site name is valid (matches standard site helpers: 1-32 chars)
func validSiteName(name string) bool {
	// Must match standard site identity constraints (same as stepanel-appctl)
	matched, _ := regexp.MatchString(`^[a-z0-9_-]{1,32}$`, name)
	// Proof sites are created only by the recovery proof, which recovery may
	// remove by name; no other workflow may claim the prefix.
	return matched && !isRecoveryProofSiteName(name)
}

// importPlacementRequest places an imported site the way an administrator's
// site creation does: in the named account, or a new account named after the
// site.
func importPlacementRequest(site, account, kind, domain string) sitePlacementRequest {
	account = strings.TrimSpace(account)
	if account == "" {
		account = site
	}
	return sitePlacementRequest{Account: account, Kind: sitelayout.Kind(strings.TrimSpace(kind)), Domain: domain}
}
