package main

import (
	"encoding/json"
	"net/http"

	"github.com/itchyitchy123/StePanel/internal/importer"
)

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

	// Phase 1: Just validate and inspect, don't actually import yet
	analyzer := importer.NewAnalyzer()
	inspection, err := analyzer.InspectArchive(req.URL, req.ConfigPath)

	if err != nil && inspection == nil {
		http.Error(w, "failed to inspect archive: "+err.Error(), http.StatusBadRequest)
		return
	}

	// TODO: Phase 2 - enqueue import job, return job ID
	response := map[string]interface{}{
		"status":     "inspection_complete",
		"inspection": inspection,
		"site_name":  req.SiteName,
		"message":    "Archive inspection successful. Review requirements and confirm import.",
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

	// TODO: Phase 2 - look up job progress
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}
