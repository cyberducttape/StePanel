package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Capability represents a single capability and its availability status
type Capability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"` // Only present if not available
}

// CapabilitiesResponse is the response from the /api/capabilities endpoint
type CapabilitiesResponse struct {
	Version      string                `json:"version"`
	Hostname     string                `json:"hostname"`
	Capabilities map[string]Capability `json:"capabilities"`
}


// ProbeCapabilities detects what the host can actually do
func (a *App) ProbeCapabilities() CapabilitiesResponse {
	hostname, _ := os.Hostname()
	return CapabilitiesResponse{
		Version:      Version,
		Hostname:     hostname,
		Capabilities: a.probeAllCapabilities(),
	}
}

func (a *App) probeAllCapabilities() map[string]Capability {
	caps := make(map[string]Capability)

	// Site lifecycle
	caps["site.lifecycle.create"] = Capability{Available: true}
	caps["site.lifecycle.delete"] = Capability{Available: true}
	caps["site.lifecycle.suspend"] = Capability{Available: true}

	// Database operations
	caps["database.mysql.create"] = a.checkDatabaseCapability("mysql")
	caps["database.mysql.restore"] = a.checkDatabaseCapability("mysql")
	caps["database.postgresql.create"] = a.checkDatabaseCapability("postgresql")
	caps["database.postgresql.restore"] = a.checkDatabaseCapability("postgresql")
	caps["database.mariadb.create"] = a.checkDatabaseCapability("mariadb")
	caps["database.mariadb.restore"] = a.checkDatabaseCapability("mariadb")

	// Archive import
	caps["archive.import.inspect"] = Capability{Available: true}
	caps["archive.import.extract"] = a.checkArchiveImportCapability()
	caps["archive.import.database_restore"] = a.checkDatabaseRestorationCapability()

	// Network capabilities
	caps["runner.network_isolation"] = a.checkNetworkIsolationCapability()
	caps["runner.registry_allowlist"] = Capability{Available: len(a.Config.RunnerAllowedRegistries) > 0}

	// Filesystem capabilities
	caps["filesystem.quotas"] = a.checkFilesystemQuotasCapability()
	caps["filesystem.symlinks"] = Capability{Available: true} // Rejection is a feature, not lack of capability

	// Backup/restore
	caps["backup.verified"] = Capability{Available: a.Config.BackupSigningKey != ""}
	caps["backup.offsite"] = a.checkOffsiteBackupCapability()
	caps["restore.to_staging"] = Capability{Available: true}
	caps["restore.verified_file"] = Capability{Available: true}
	caps["restore.database_only"] = Capability{Available: true}

	// Authentication
	caps["auth.mfa"] = Capability{Available: a.Auth.TOTPEnabled}
	caps["auth.api_tokens"] = Capability{Available: true}
	caps["auth.scoped_tokens"] = Capability{Available: true}

	// Deployment
	caps["deployment.git"] = Capability{Available: true}
	caps["deployment.builds"] = a.checkBuildCapability()

	// Mail (optional)
	caps["mail.integration"] = a.checkMailCapability()

	// DNS
	caps["dns.management"] = a.checkDNSCapability()

	return caps
}

func (a *App) checkDatabaseCapability(dbType string) Capability {
	// Check if database binaries are available in PATH
	var binaries []string
	switch dbType {
	case "mysql":
		binaries = []string{"mysql", "mysqldump"}
	case "mariadb":
		binaries = []string{"mysql", "mysqldump"}
	case "postgresql":
		binaries = []string{"psql", "pg_dump"}
	}

	for _, bin := range binaries {
		if _, err := exec.LookPath(bin); err != nil {
			return Capability{
				Available: false,
				Reason:    fmt.Sprintf("%s command not found in PATH", bin),
			}
		}
	}

	return Capability{Available: true}
}

func (a *App) checkArchiveImportCapability() Capability {
	// Archive import always available - it's implemented in Phase 1
	return Capability{Available: true}
}

func (a *App) checkDatabaseRestorationCapability() Capability {
	// Phase 1 has manual restoration (database dump located)
	// Check if we have database tools to execute restoration
	if _, err := exec.LookPath("mysql"); err == nil {
		return Capability{
			Available: true,
		}
	}
	if _, err := exec.LookPath("psql"); err == nil {
		return Capability{
			Available: true,
		}
	}

	return Capability{
		Available: false,
		Reason:    "no database restore tools (mysql or psql) found in PATH",
	}
}

func (a *App) checkNetworkIsolationCapability() Capability {
	// Check if podman supports --network none with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "podman", "run", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return Capability{
			Available: false,
			Reason:    "podman not available on system",
		}
	}

	if strings.Contains(string(output), "--network") {
		return Capability{Available: true}
	}

	return Capability{
		Available: false,
		Reason:    "podman version does not support --network flag",
	}
}

func (a *App) checkFilesystemQuotasCapability() Capability {
	// Check if quota tools are available
	if _, err := exec.LookPath("quotactl"); err == nil {
		return Capability{Available: true}
	}

	// Check /proc/mounts for filesystems with quota support
	mounts, err := os.ReadFile("/proc/mounts")
	if err == nil && strings.Contains(string(mounts), "usrquota") {
		return Capability{Available: true}
	}

	return Capability{
		Available: false,
		Reason:    "filesystem not mounted with usrquota; quotactl not available",
	}
}

func (a *App) checkOffsiteBackupCapability() Capability {
	if a.Config.OffsiteTarget != "" {
		// Check if rclone is configured
		if _, err := exec.LookPath("rclone"); err == nil {
			return Capability{Available: true}
		}
		return Capability{
			Available: false,
			Reason:    "offsite target configured but rclone not found in PATH",
		}
	}
	return Capability{
		Available: false,
		Reason:    "STEPANEL_OFFSITE_TARGET not configured",
	}
}

func (a *App) checkBuildCapability() Capability {
	if _, err := exec.LookPath("podman"); err != nil {
		return Capability{
			Available: false,
			Reason:    "podman not available on system",
		}
	}
	return Capability{Available: true}
}

func (a *App) checkMailCapability() Capability {
	// Mail is operator-integrated only, not a core StePanel capability
	// Check if exim4 or postfix is available
	if _, err := exec.LookPath("exim4"); err == nil {
		return Capability{Available: true}
	}
	if _, err := exec.LookPath("postfix"); err == nil {
		return Capability{Available: true}
	}

	return Capability{
		Available: false,
		Reason:    "mail integration not configured; exim4 or postfix not found",
	}
}

func (a *App) checkDNSCapability() Capability {
	// DNS capabilities depend on provider integrations (Linode, etc.)
	// For now, core DNS validation is always available
	return Capability{
		Available: true,
		// Note: provider-specific DNS mutations require provider credentials
	}
}

// handleCapabilities returns a JSON response of host capabilities
func (a *App) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Capabilities endpoint is public (read-only, no sensitive data)
	capabilities := a.ProbeCapabilities()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(capabilities); err != nil {
		// Encoding errors are not recoverable at this point (response already started)
		// but we should at least log them in a production system
		return
	}
}
