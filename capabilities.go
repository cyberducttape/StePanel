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

// CapabilityMode represents the operational state of a capability
type CapabilityMode string

const (
	CapabilityUnsupported CapabilityMode = "unsupported" // Feature not available
	CapabilityManual      CapabilityMode = "manual"      // Feature requires manual operation
	CapabilityPartial     CapabilityMode = "partial"     // Feature partially implemented
	CapabilityAvailable   CapabilityMode = "available"   // Feature fully operational
	CapabilityDegraded    CapabilityMode = "degraded"    // Feature operational but reduced
)

// Capability represents a single capability and its operational state
type Capability struct {
	Available bool           `json:"available"` // Backward compat: true if mode is "available" or "partial"
	Mode      CapabilityMode `json:"mode"`      // Operational state: unsupported, manual, partial, available, degraded
	Reason    string         `json:"reason,omitempty"`
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
	caps["site.lifecycle.create"] = Capability{Available: true, Mode: CapabilityAvailable}
	caps["site.lifecycle.delete"] = Capability{Available: true, Mode: CapabilityAvailable}
	caps["site.lifecycle.suspend"] = Capability{Available: true, Mode: CapabilityAvailable}

	// Database operations
	caps["database.mysql.create"] = a.checkDatabaseCapability("mysql")
	caps["database.mysql.restore"] = a.checkDatabaseCapability("mysql")
	caps["database.postgresql.create"] = a.checkDatabaseCapability("postgresql")
	caps["database.postgresql.restore"] = a.checkDatabaseCapability("postgresql")
	caps["database.mariadb.create"] = a.checkDatabaseCapability("mariadb")
	caps["database.mariadb.restore"] = a.checkDatabaseCapability("mariadb")

	// Archive import
	caps["archive.import.inspect"] = Capability{Available: true, Mode: CapabilityAvailable}
	caps["archive.import.extract"] = a.checkArchiveImportCapability()
	caps["archive.import.database_restore"] = a.checkDatabaseRestorationCapability()

	// Network capabilities
	caps["runner.network_isolation"] = a.checkNetworkIsolationCapability()
	caps["runner.registry_allowlist"] = Capability{Available: len(a.Config.RunnerAllowedRegistries) > 0, Mode: func() CapabilityMode {
		if len(a.Config.RunnerAllowedRegistries) > 0 {
			return CapabilityAvailable
		}
		return CapabilityUnsupported
	}()}

	// Filesystem capabilities
	caps["filesystem.quotas"] = a.checkFilesystemQuotasCapability()
	caps["filesystem.symlinks"] = Capability{Available: true, Mode: CapabilityAvailable, Reason: "Symlink rejection enforced for security"}

	// Backup/restore
	caps["backup.verified"] = Capability{
		Available: a.Config.BackupSigningKey != "",
		Mode: func() CapabilityMode {
			if a.Config.BackupSigningKey != "" {
				return CapabilityAvailable
			}
			return CapabilityUnsupported
		}(),
	}
	caps["backup.offsite"] = a.checkOffsiteBackupCapability()
	caps["restore.to_staging"] = Capability{Available: true, Mode: CapabilityAvailable}
	caps["restore.verified_file"] = Capability{Available: true, Mode: CapabilityAvailable}
	caps["restore.database_only"] = Capability{Available: true, Mode: CapabilityAvailable}

	// Authentication
	caps["auth.mfa"] = Capability{
		Available: a.Auth.TOTPEnabled,
		Mode: func() CapabilityMode {
			if a.Auth.TOTPEnabled {
				return CapabilityAvailable
			}
			return CapabilityUnsupported
		}(),
	}
	caps["auth.api_tokens"] = Capability{Available: true, Mode: CapabilityAvailable}
	caps["auth.scoped_tokens"] = Capability{Available: true, Mode: CapabilityAvailable}

	// Deployment
	caps["deployment.git"] = Capability{Available: true, Mode: CapabilityAvailable}
	caps["deployment.builds"] = a.checkBuildCapability()

	// Mail (optional)
	caps["mail.integration"] = a.checkMailCapability()

	// DNS
	caps["dns.management"] = a.checkDNSCapability()

	return caps
}

func (a *App) checkDatabaseCapability(dbType string) Capability {
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
				Mode:      CapabilityUnsupported,
				Reason:    fmt.Sprintf("%s command not found in PATH", bin),
			}
		}
	}

	return Capability{Available: true, Mode: CapabilityAvailable}
}

func (a *App) checkArchiveImportCapability() Capability {
	// Archive import always available - it's implemented in Phase 1
	return Capability{Available: true, Mode: CapabilityAvailable}
}

func (a *App) checkDatabaseRestorationCapability() Capability {
	// Phase 1 (v0.7.0): Manual restoration - operator restores database using provided dump
	// Phase 2 (v0.8.0): Automated restoration via SiteManager lifecycle
	hasMySQL := exec.Command("which", "mysql").Run() == nil
	hasPSQL := exec.Command("which", "psql").Run() == nil

	if hasMySQL || hasPSQL {
		return Capability{
			Available: true,
			Mode:      CapabilityManual,
			Reason:    "Archive import locates database dump; operator restores manually. Automated restoration planned for v0.8.",
		}
	}

	return Capability{
		Available: false,
		Mode:      CapabilityUnsupported,
		Reason:    "Archive import database restoration unavailable: no database restore tools (mysql or psql) found in PATH",
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
			Mode:      CapabilityUnsupported,
			Reason:    "podman not available on system",
		}
	}

	if strings.Contains(string(output), "--network") {
		return Capability{Available: true, Mode: CapabilityAvailable}
	}

	return Capability{
		Available: false,
		Mode:      CapabilityUnsupported,
		Reason:    "podman version does not support --network flag",
	}
}

func (a *App) checkFilesystemQuotasCapability() Capability {
	if _, err := exec.LookPath("quotactl"); err == nil {
		return Capability{Available: true, Mode: CapabilityAvailable}
	}

	mounts, err := os.ReadFile("/proc/mounts")
	if err == nil && strings.Contains(string(mounts), "usrquota") {
		return Capability{Available: true, Mode: CapabilityAvailable}
	}

	return Capability{
		Available: false,
		Mode:      CapabilityUnsupported,
		Reason:    "filesystem not mounted with usrquota; quotactl not available",
	}
}

func (a *App) checkOffsiteBackupCapability() Capability {
	if a.Config.OffsiteTarget != "" {
		if _, err := exec.LookPath("rclone"); err == nil {
			return Capability{Available: true, Mode: CapabilityAvailable}
		}
		return Capability{
			Available: false,
			Mode:      CapabilityDegraded,
			Reason:    "offsite target configured but rclone not found in PATH",
		}
	}
	return Capability{
		Available: false,
		Mode:      CapabilityUnsupported,
		Reason:    "STEPANEL_OFFSITE_TARGET not configured",
	}
}

func (a *App) checkBuildCapability() Capability {
	if _, err := exec.LookPath("podman"); err != nil {
		return Capability{
			Available: false,
			Mode:      CapabilityUnsupported,
			Reason:    "podman not available on system",
		}
	}
	return Capability{Available: true, Mode: CapabilityAvailable}
}

func (a *App) checkMailCapability() Capability {
	if _, err := exec.LookPath("exim4"); err == nil {
		return Capability{Available: true, Mode: CapabilityAvailable}
	}
	if _, err := exec.LookPath("postfix"); err == nil {
		return Capability{Available: true, Mode: CapabilityAvailable}
	}

	return Capability{
		Available: false,
		Mode:      CapabilityUnsupported,
		Reason:    "mail integration not configured; exim4 or postfix not found",
	}
}

func (a *App) checkDNSCapability() Capability {
	return Capability{
		Available: true,
		Mode:      CapabilityAvailable,
		Reason:    "Core DNS validation available; provider-specific mutations require provider credentials",
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
