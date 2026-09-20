package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/itchyitchy123/StePanel/internal/importer"
)

// durableArchiveImportRequest is the job payload for archive imports
type durableArchiveImportRequest struct {
	ArchiveURL string `json:"archive_url"`
	ConfigPath string `json:"config_path"`
	SiteName   string `json:"site_name"`
	WebRoot    string `json:"web_root"`
}

// archiveInspectionRequest is the API request to inspect an archive
type archiveInspectionRequest struct {
	URL        string `json:"url"`         // URL to archive (S3, HTTP, etc.)
	ConfigPath string `json:"config_path"` // path to config file within archive
}

// archiveImportRequest is the API request to start importing from archive
type archiveImportRequest struct {
	URL              string `json:"url"`                // URL to archive
	ConfigPath       string `json:"config_path"`       // path to config file
	SiteName         string `json:"site_name"`         // name for new site
	SkipAnalysis     bool   `json:"skip_analysis"`     // if true, import directly
	ExtractDirectory string `json:"extract_directory"` // extraction target
}

// archiveInspectionStatus is the response showing inspection results
type archiveInspectionStatus struct {
	Status      string                    `json:"status"` // "pending", "analyzing", "done", "failed"
	Inspection  *importer.ArchiveInspection `json:"inspection,omitempty"`
	Error       string                    `json:"error,omitempty"`
	CompletedAt string                    `json:"completed_at,omitempty"`
}

func (a *App) inspectArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Only administrators can inspect archives
	if !a.Auth.IsAdministrator(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	var req archiveInspectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.URL == "" || req.ConfigPath == "" {
		http.Error(w, "url and config_path are required", http.StatusBadRequest)
		return
	}

	// Perform inspection synchronously for now (Phase 1)
	analyzer := importer.NewAnalyzer()
	inspection, err := analyzer.InspectArchive(req.URL, req.ConfigPath)

	if err != nil && inspection == nil {
		http.Error(w, "failed to inspect archive: "+err.Error(), http.StatusBadRequest)
		return
	}

	response := archiveInspectionStatus{
		Status:      "done",
		Inspection:  inspection,
	}

	if err != nil {
		response.Status = "failed"
		response.Error = err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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

	inspectionID := r.URL.Query().Get("id")
	if inspectionID == "" {
		http.Error(w, "inspection id required", http.StatusBadRequest)
		return
	}

	// TODO: Phase 2 - implement job status tracking
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (a *App) archiveImportStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !a.Auth.IsAdministrator(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	var req archiveImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.URL == "" || req.ConfigPath == "" {
		http.Error(w, "url and config_path are required", http.StatusBadRequest)
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

	// Phase 1: Inspect archive first
	analyzer := importer.NewAnalyzer()
	inspection, err := analyzer.InspectArchive(req.URL, req.ConfigPath)

	if err != nil && inspection == nil {
		http.Error(w, "failed to inspect archive: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Phase 2: Enqueue import job
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

	response := map[string]interface{}{
		"status":     "inspection_complete",
		"job_id":     job.ID,
		"inspection": inspection,
		"site_name":  req.SiteName,
		"message":    "Archive inspection successful and import job queued.",
	}

	if err != nil {
		response["warning"] = err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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
	json.NewEncoder(w).Encode(response)
}

// handleArchiveImportJob processes an archive import durable job
func (a *App) handleArchiveImportJob(ctx context.Context, job *Job) error {
	var req durableArchiveImportRequest
	if err := json.Unmarshal(job.Payload, &req); err != nil {
		return fmt.Errorf("failed to parse job payload: %w", err)
	}

	// Create executor and perform import
	executor := importer.NewExecutor()

	// Wrapper to capture progress updates
	progressUpdates := make([]map[string]interface{}, 0)

	result, err := executor.ExecuteImport(ctx, &importer.ArchiveImportRequest{
		URL:              req.ArchiveURL,
		ConfigPath:       req.ConfigPath,
		SiteName:         req.SiteName,
		SkipAnalysis:     false,
		ExtractDirectory: "",
	}, req.WebRoot, func(importJob *importer.ImportJob) {
		// Capture progress update
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
		return fmt.Errorf("archive import failed: %w", err)
	}

	// Store result in job output (marshal to JSON)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}
	job.Output = resultJSON

	return nil
}

// validSiteName checks if a site name is valid
func validSiteName(name string) bool {
	// Allow lowercase letters, numbers, hyphens
	// Must be 1-63 characters
	matched, _ := regexp.MatchString(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`, name)
	return matched && len(name) > 0 && len(name) <= 63
}
