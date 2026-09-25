package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/cyberducttape/StePanel/internal/importer"
)

// durableArchiveImportRequest is the job payload for archive imports
type durableArchiveImportRequest struct {
	ArchiveURL string `json:"archive_url"`
	ConfigPath string `json:"config_path"`
	SiteName   string `json:"site_name"`
	WebRoot    string `json:"web_root"`
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
	URL        string `json:"url"`         // URL to archive
	ConfigPath string `json:"config_path"` // path to config file
	SiteName   string `json:"site_name"`   // name for new site
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
		ArchiveURL: req.URL,
		ConfigPath: req.ConfigPath,
		SiteName:   req.SiteName,
		WebRoot:    a.Config.WebRoot,
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
	_ = json.NewEncoder(w).Encode(response)
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
	if req.WebRoot == "" {
		return errors.New("archive import payload is missing web root")
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

	releaseSite := a.siteOperations.Acquire(req.SiteName)
	defer releaseSite()

	canonical := filepath.Join(req.WebRoot, "sites", req.SiteName, "public")
	if _, err := os.Stat(canonical); err == nil {
		return fmt.Errorf("site %q already exists", req.SiteName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect canonical site: %w", err)
	}

	stagingParent := filepath.Join(req.WebRoot, "sites", ".import-staging")
	if err := os.MkdirAll(stagingParent, 0750); err != nil {
		return fmt.Errorf("prepare staging root: %w", err)
	}
	stagingDir := filepath.Join(stagingParent, fmt.Sprintf("%s-%s", req.SiteName, job.ID))
	// Fail if a leftover collision exists rather than silently reusing it.
	if _, err := os.Stat(stagingDir); err == nil {
		return fmt.Errorf("staging directory %q already exists", stagingDir)
	}

	activated := false
	defer func() {
		if !activated {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	executor := importer.NewExecutor()
	progressUpdates := make([]map[string]interface{}, 0)

	result, err := executor.ExecuteImport(ctx, &importer.ArchiveImportRequest{
		URL:        req.ArchiveURL,
		ConfigPath: req.ConfigPath,
		SiteName:   req.SiteName,
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

	if err := os.MkdirAll(filepath.Dir(canonical), 0750); err != nil {
		return fmt.Errorf("prepare canonical site parent: %w", err)
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

	if err := os.Rename(stagingDir, canonical); err != nil {
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

	if result != nil {
		result.SiteStatus = "ready"
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
