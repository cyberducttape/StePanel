package main

import (
	"encoding/json"
	"fmt"
	"github.com/cyberducttape/StePanel/internal/doctor"
	"net/http"
	"os"
	"strings"
)

// migrationAnalysisRequest is the input for migration analysis
type migrationAnalysisRequest struct {
	SourceHostname      string `json:"source_hostname"`
	SourceSSHHost       string `json:"source_ssh_host"`
	SourceSSHPort       int    `json:"source_ssh_port"`
	SourceSSHUser       string `json:"source_ssh_user"`
	SourceSSHKey        string `json:"source_ssh_key,omitempty"` // Base64-encoded private key
	DestinationHostname string `json:"destination_hostname"`
}

// migrationAnalysisResponse is the analysis result
type migrationAnalysisResponse struct {
	Analysis doctor.MigrationAnalysis `json:"analysis"`
}

// ProductionReadinessCheck represents a single production readiness check
type ProductionReadinessCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`     // "pass", "warning", "fail"
	Severity string `json:"severity"`   // "info", "warning", "critical"
	Message  string `json:"message,omitempty"`
	Remediation string `json:"remediation,omitempty"`
}

// ProductionReadinessReport represents overall production readiness
type ProductionReadinessReport struct {
	IsProduction bool                       `json:"is_production"`
	Checks       []ProductionReadinessCheck `json:"checks"`
	OverallStatus string                   `json:"overall_status"` // "healthy", "degraded", "critical"
}

// productionReadiness handles production readiness checks
func (a *App) productionReadiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Only administrators can view production readiness
	if !a.Auth.IsAdministrator(r) {
		http.Error(w, "unauthorized", http.StatusForbidden)
		return
	}

	report := a.checkProductionReadiness()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(report); err != nil {
		return
	}
}

// checkProductionReadiness performs comprehensive production readiness checks
func (a *App) checkProductionReadiness() ProductionReadinessReport {
	report := ProductionReadinessReport{
		IsProduction: a.Config.Production,
		Checks:       []ProductionReadinessCheck{},
	}

	// Only perform detailed checks in production mode
	if !a.Config.Production {
		report.OverallStatus = "healthy"
		report.Checks = append(report.Checks, ProductionReadinessCheck{
			Name:     "Production Mode",
			Status:   "pass",
			Severity: "info",
			Message:  "Running in non-production mode (development/lab/test)",
		})
		return report
	}

	// Production mode checks
	checks := []ProductionReadinessCheck{
		a.checkFilesystemQuotaReadiness(),
		a.checkEncryptionKeysReadiness(),
		a.checkOfflineBackupReadiness(),
		a.checkTLSReadiness(),
	}

	report.Checks = checks

	// Determine overall status
	criticalCount := 0
	warningCount := 0
	for _, check := range checks {
		if check.Status == "fail" {
			criticalCount++
		} else if check.Status == "warning" {
			warningCount++
		}
	}

	if criticalCount > 0 {
		report.OverallStatus = "critical"
	} else if warningCount > 0 {
		report.OverallStatus = "degraded"
	} else {
		report.OverallStatus = "healthy"
	}

	return report
}

// checkFilesystemQuotaReadiness checks if filesystem quotas are enforced
func (a *App) checkFilesystemQuotaReadiness() ProductionReadinessCheck {
	if a.Config.WebRoot == "" {
		return ProductionReadinessCheck{
			Name:     "Filesystem Quotas",
			Status:   "fail",
			Severity: "critical",
			Message:  "STEPANEL_WEB_ROOT is not configured",
		}
	}

	// Check if WebRoot has quota support
	hasQuotas := false
	if mounts, err := os.ReadFile("/proc/mounts"); err == nil {
		for _, line := range strings.Split(string(mounts), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 4 && strings.HasPrefix(a.Config.WebRoot, fields[1]) {
				opts := fields[3]
				if strings.Contains(opts, "usrquota") || strings.Contains(opts, "grpquota") || strings.Contains(opts, "prjquota") {
					hasQuotas = true
					break
				}
			}
		}
	}

	if hasQuotas {
		return ProductionReadinessCheck{
			Name:     "Filesystem Quotas",
			Status:   "pass",
			Severity: "info",
			Message:  "Filesystem quota enforcement enabled on STEPANEL_WEB_ROOT",
		}
	}

	return ProductionReadinessCheck{
		Name:     "Filesystem Quotas",
		Status:   "fail",
		Severity: "critical",
		Message:  "STEPANEL_WEB_ROOT does not have quota support enabled (usrquota/grpquota/prjquota)",
		Remediation: "Enable quotas on mount: mount -o remount,usrquota /var/www or update /etc/fstab",
	}
}

// checkEncryptionKeysReadiness checks encryption key configuration
func (a *App) checkEncryptionKeysReadiness() ProductionReadinessCheck {
	if a.Config.EnvironmentKey == "" || a.Config.BackupSigningKey == "" {
		return ProductionReadinessCheck{
			Name:     "Encryption Keys",
			Status:   "fail",
			Severity: "critical",
			Message:  "Required encryption keys not configured (STEPANEL_ENVIRONMENT_KEY or STEPANEL_BACKUP_SIGNING_KEY)",
		}
	}

	return ProductionReadinessCheck{
		Name:     "Encryption Keys",
		Status:   "pass",
		Severity: "info",
		Message:  "Required encryption keys are configured",
	}
}

// checkOfflineBackupReadiness checks offsite backup configuration
func (a *App) checkOfflineBackupReadiness() ProductionReadinessCheck {
	if !a.Config.RequireOffsiteBackup || a.Config.OffsiteTarget == "" {
		return ProductionReadinessCheck{
			Name:     "Offsite Backups",
			Status:   "fail",
			Severity: "critical",
			Message:  "Offsite backup enforcement not configured",
			Remediation: "Set STEPANEL_REQUIRE_OFFSITE_BACKUP=1 and configure STEPANEL_OFFSITE_TARGET",
		}
	}

	return ProductionReadinessCheck{
		Name:     "Offsite Backups",
		Status:   "pass",
		Severity: "info",
		Message:  "Offsite backups required and configured",
	}
}

// checkTLSReadiness checks TLS configuration
func (a *App) checkTLSReadiness() ProductionReadinessCheck {
	if a.Config.TLSCertFile == "" || a.Config.TLSKeyFile == "" {
		if !a.Config.TLSAlreadyTerminated {
			return ProductionReadinessCheck{
				Name:     "TLS/HTTPS",
				Status:   "fail",
				Severity: "critical",
				Message:  "Application TLS not configured and TLS not terminated by reverse proxy",
				Remediation: "Either provide STEPANEL_TLS_CERT_FILE and STEPANEL_TLS_KEY_FILE, or set STEPANEL_TLS_TERMINATED=1 if using a reverse proxy",
			}
		}

		return ProductionReadinessCheck{
			Name:     "TLS/HTTPS",
			Status:   "pass",
			Severity: "info",
			Message:  "TLS terminated by reverse proxy",
		}
	}

	return ProductionReadinessCheck{
		Name:     "TLS/HTTPS",
		Status:   "pass",
		Severity: "info",
		Message:  "Application TLS configured",
	}
}

// migrationDoctor handles migration analysis requests
func (a *App) migrationDoctor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Only administrators can use Migration Doctor
	if !a.Auth.IsAdministrator(r) {
		http.Error(w, "unauthorized", http.StatusForbidden)
		return
	}

	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	var req migrationAnalysisRequest
	if err := decodeJSON(w, r, 8192, &req); err != nil {
		return
	}

	// Validate request
	if req.SourceSSHHost == "" {
		http.Error(w, "source_ssh_host is required", http.StatusBadRequest)
		return
	}

	if req.SourceSSHPort == 0 {
		req.SourceSSHPort = 22
	}

	if req.SourceSSHUser == "" {
		req.SourceSSHUser = "root"
	}

	// Start analysis job
	jobPayload, err := json.Marshal(req)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	job, err := a.Jobs.Enqueue("migration.analysis", "", "", jobPayload, 1)
	if err != nil {
		http.Error(w, "could not create analysis job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":  job.ID,
		"status":  "analysis_in_progress",
		"message": fmt.Sprintf("Analyzing migration from %s to %s", req.SourceSSHHost, req.DestinationHostname),
	})
}

// migrationAnalysisStatus returns the current status of an analysis job
func (a *App) migrationAnalysisStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !a.Auth.IsAdministrator(r) {
		http.Error(w, "unauthorized", http.StatusForbidden)
		return
	}

	jobID := strings.TrimSpace(r.URL.Query().Get("job_id"))
	if jobID == "" {
		http.Error(w, "job_id parameter required", http.StatusBadRequest)
		return
	}

	job, exists := a.Jobs.Get(jobID)
	if !exists {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	response := map[string]any{
		"job_id": jobID,
		"state":  job.State,
		"kind":   job.Kind,
	}

	if job.State == "done" {
		if len(job.Output) > 0 {
			var result migrationAnalysisResponse
			if err := json.Unmarshal(job.Output, &result); err == nil {
				response["analysis"] = result.Analysis
			}
		}
	} else if job.State == "failed" {
		response["error"] = string(job.Output)
	}

	writeJSON(w, http.StatusOK, response)
}

// handleMigrationAnalysisJob executes a migration analysis in the background
func (a *App) handleMigrationAnalysisJob(r *Job) ([]byte, error) {
	var req migrationAnalysisRequest
	if err := json.Unmarshal(r.Payload, &req); err != nil {
		return nil, fmt.Errorf("decode migration analysis request: %w", err)
	}

	// For now, return a mock analysis
	// In production, this would SSH to the source server and scan it
	sourceInv := a.mockServerInventory("source")
	destInv := a.mockServerInventory("destination")

	analyzer := doctor.NewAnalyzer()
	analysis := analyzer.Analyze(sourceInv, destInv)

	result := migrationAnalysisResponse{
		Analysis: *analysis,
	}

	output, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode migration analysis result: %w", err)
	}

	// Audit the analysis
	recordAudit(a.Config.AuditLog, "admin", "migration.analysis.completed",
		fmt.Sprintf("%s -> %s", req.SourceSSHHost, req.DestinationHostname),
		fmt.Sprintf("blockers=%d, warnings=%d", len(analysis.Blockers), len(analysis.Warnings)))

	return output, nil
}

// mockServerInventory creates a mock inventory for demo purposes
// In production, this would use SSH to scan the actual server
func (a *App) mockServerInventory(label string) doctor.ServerInventory {
	if label == "source" {
		return doctor.ServerInventory{
			Hostname: "source.example.com",
			OS: doctor.OperatingSystem{
				Name:          "CentOS",
				Version:       "7.9",
				DistributorID: "centos",
				Architecture:  "x86_64",
			},
			PHP: doctor.PHPRuntime{
				Version: "7.4",
				Extensions: []string{
					"core", "standard", "date", "pcre", "json", "mysqli", "pdo",
					"curl", "mbstring", "openssl", "redis", "igbinary",
				},
				FPMVersion:   "7.4",
				MaxUploadMB:  512,
				MemoryLimit:  256,
				MaxExecution: 300,
				TimeZone:     "UTC",
			},
			Database: doctor.DatabaseSystem{
				Type:      "MySQL",
				Version:   "5.7.34",
				Encoding:  "utf8mb4",
				SQLMode:   "STRICT_TRANS_TABLES,NO_ZERO_DATE,NO_ZERO_IN_DATE,ERROR_FOR_DIVISION_BY_ZERO",
				Databases: 15,
				Reachable: true,
			},
			WebServer: doctor.WebServerInfo{
				Type:           "Apache",
				Version:        "2.4.6",
				MaxConnections: 256,
				DocumentRoot:   "/home/users",
				VirtualHosts:   8,
			},
			SystemResources: doctor.SystemResources{
				TotalDiskGB:     500,
				AvailableDiskGB: 250,
				TotalMemoryGB:   8,
				CPUCores:        4,
				Swap:            2048,
			},
			Sites: []doctor.SiteInfo{
				{
					Domain:             "example.com",
					DocumentRoot:       "/home/users/example.com/public_html",
					DiskUsageMB:        1500,
					FileCount:          12450,
					DatabaseNames:      []string{"example_prod"},
					HasHTAccess:        true,
					HasSSL:             true,
					Application:        "WordPress",
					ApplicationVersion: "5.8.1",
				},
			},
			ExternalServices: []doctor.ExternalService{
				{Type: "SMTP", Description: "SendGrid", Host: "smtp.sendgrid.net", Port: 587, Reachable: true},
				{Type: "Redis", Description: "Redis Cache", Host: "localhost", Port: 6379, Reachable: true},
			},
		}
	}

	// Destination inventory
	return doctor.ServerInventory{
		Hostname: "destination.example.com",
		OS: doctor.OperatingSystem{
			Name:          "AlmaLinux",
			Version:       "9.0",
			DistributorID: "almalinux",
			Architecture:  "x86_64",
		},
		PHP: doctor.PHPRuntime{
			Version: "8.2",
			Extensions: []string{
				"core", "standard", "date", "pcre", "json", "mysqli", "pdo",
				"curl", "mbstring", "openssl", "redis",
			},
			FPMVersion:   "8.2",
			MaxUploadMB:  512,
			MemoryLimit:  512,
			MaxExecution: 300,
			TimeZone:     "UTC",
		},
		Database: doctor.DatabaseSystem{
			Type:      "MySQL",
			Version:   "8.0.28",
			Encoding:  "utf8mb4",
			SQLMode:   "STRICT_TRANS_TABLES,NO_ZERO_DATE,NO_ZERO_IN_DATE,ERROR_FOR_DIVISION_BY_ZERO",
			Databases: 0,
			Reachable: true,
		},
		WebServer: doctor.WebServerInfo{
			Type:           "Apache",
			Version:        "2.4.51",
			MaxConnections: 512,
			DocumentRoot:   "/var/www",
			VirtualHosts:   0,
		},
		SystemResources: doctor.SystemResources{
			TotalDiskGB:     1000,
			AvailableDiskGB: 850,
			TotalMemoryGB:   16,
			CPUCores:        8,
			Swap:            4096,
		},
		Sites: []doctor.SiteInfo{},
		ExternalServices: []doctor.ExternalService{
			{Type: "SMTP", Description: "SendGrid", Host: "smtp.sendgrid.net", Port: 587, Reachable: true},
			{Type: "Redis", Description: "Redis Cache", Host: "localhost", Port: 6379, Reachable: true},
		},
	}
}
