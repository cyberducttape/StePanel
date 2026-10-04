package stepanel

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"time"
)

// supportBundleJob is deliberately smaller than Job. Durable payloads and
// outputs can contain credentials, archive metadata, or customer content and
// must never be copied into a support artifact.
type supportBundleJob struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Owner      string     `json:"owner"`
	State      string     `json:"state"`
	Progress   int        `json:"progress,omitempty"`
	Attempts   int        `json:"attempts,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type supportBundleConfig struct {
	WebServer             string `json:"web_server"`
	DBEngine              string `json:"database_engine"`
	DBVersion             string `json:"database_version"`
	Production            bool   `json:"production"`
	WorkerMode            string `json:"worker_mode"`
	CloudProvider         string `json:"cloud_provider,omitempty"`
	RequireOffsiteBackup  bool   `json:"require_offsite_backup"`
	TLSAlreadyTerminated  bool   `json:"tls_already_terminated"`
	RunnerNetworkMode     string `json:"runner_network_mode"`
	MaxUploadBytes        int64  `json:"max_upload_bytes"`
	MaxEntries            int    `json:"max_entries"`
	MaxConcurrentJobs     int    `json:"max_concurrent_jobs"`
	MinimumFreeBytes      uint64 `json:"minimum_free_bytes"`
	StageRetentionHours   int    `json:"stage_retention_hours"`
	GitReleaseRetention   int    `json:"git_release_retention"`
	GitReleaseMaxAgeHours int    `json:"git_release_max_age_hours"`
}

func (a *App) supportBundle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator access required", http.StatusForbidden)
		return
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="stepanel-support-%s.zip"`, stamp))
	if err := writeSupportBundle(w, a); err != nil {
		// The response has already started once the ZIP header is written. Keep
		// the failure server-side rather than appending invalid JSON to a ZIP.
		logSupportBundleError(err)
	}
}

func writeSupportBundle(out io.Writer, a *App) error {
	archive := zip.NewWriter(out)
	closeArchive := func(err error) error {
		if closeErr := archive.Close(); err == nil {
			err = closeErr
		}
		return err
	}

	writeJSON := func(name string, value any) error {
		entry, err := archive.Create(name)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(entry)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	writeText := func(name, value string) error {
		entry, err := archive.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(entry, value)
		return err
	}

	generated := time.Now().UTC()
	if err := writeText("README.txt", "StePanel support bundle\n\nThis bundle contains redacted operational diagnostics. It does not contain passwords, encryption keys, TOTP seeds, database contents, job payloads, job outputs, or raw customer logs.\n"); err != nil {
		return closeArchive(err)
	}
	if err := writeJSON("metadata.json", map[string]any{
		"generated_at": generated,
		"version":      Version,
		"commit":       Commit,
		"go_version":   runtime.Version(),
		"goos":         runtime.GOOS,
		"goarch":       runtime.GOARCH,
	}); err != nil {
		return closeArchive(err)
	}
	if err := writeJSON("configuration.json", supportBundleConfig{
		WebServer:             a.Config.WebServer,
		DBEngine:              a.Config.DBEngine,
		DBVersion:             a.Config.DBVersion,
		Production:            a.Config.Production,
		WorkerMode:            a.Config.WorkerMode,
		CloudProvider:         a.Config.CloudProvider,
		RequireOffsiteBackup:  a.Config.RequireOffsiteBackup,
		TLSAlreadyTerminated:  a.Config.TLSAlreadyTerminated,
		RunnerNetworkMode:     a.Config.RunnerNetworkMode,
		MaxUploadBytes:        a.Config.MaxUpload,
		MaxEntries:            a.Config.MaxEntries,
		MaxConcurrentJobs:     a.Config.MaxConcurrentJobs,
		MinimumFreeBytes:      a.Config.MinFreeBytes,
		StageRetentionHours:   a.Config.StageRetentionHours,
		GitReleaseRetention:   a.Config.GitReleaseRetention,
		GitReleaseMaxAgeHours: a.Config.GitReleaseMaxAgeHours,
	}); err != nil {
		return closeArchive(err)
	}

	readiness := readinessChecks(a.Config, a.Jobs)
	readinessSummary := make(map[string]bool, len(readiness))
	for name, check := range readiness {
		readinessSummary[name] = check.Ready
	}
	if err := writeJSON("readiness.json", map[string]any{"ready": allReadinessChecksPass(readiness), "checks": readinessSummary}); err != nil {
		return closeArchive(err)
	}
	operational := operationalChecks(a.Config, a.Jobs)
	operationalSummary := make(map[string]bool, len(operational))
	for name, check := range operational {
		operationalSummary[name] = check.Ready
	}
	if err := writeJSON("operational-readiness.json", map[string]any{"operational": allReadinessChecksPass(operational), "checks": operationalSummary}); err != nil {
		return closeArchive(err)
	}
	if err := writeJSON("production-readiness.json", a.checkProductionReadiness()); err != nil {
		return closeArchive(err)
	}

	var metricsText bytes.Buffer
	if a.Metrics != nil {
		a.Metrics.Write(&metricsText)
	}
	writeReadinessMetrics(&metricsText, readiness)
	writeJobMetrics(&metricsText, a.Jobs)
	if err := writeText("metrics.txt", metricsText.String()); err != nil {
		return closeArchive(err)
	}

	jobs := make([]supportBundleJob, 0)
	if a.Jobs != nil {
		for _, item := range a.Jobs.List(100) {
			jobs = append(jobs, supportBundleJob{ID: item.ID, Kind: item.Kind, Owner: item.User, State: item.State, Progress: item.Progress, Attempts: item.Attempts, CreatedAt: item.StartedAt, FinishedAt: item.FinishedAt})
		}
	}
	if err := writeJSON("jobs.json", map[string]any{"jobs": jobs}); err != nil {
		return closeArchive(err)
	}
	return closeArchive(nil)
}

func allReadinessChecksPass(checks map[string]ReadinessCheck) bool {
	for _, check := range checks {
		if !check.Ready {
			return false
		}
	}
	return true
}

func logSupportBundleError(err error) {
	// Kept as a helper so the handler never writes internal diagnostics to a
	// client after the streaming response has begun.
	if err != nil {
		log.Printf("support bundle generation failed: %v", err)
	}
}
