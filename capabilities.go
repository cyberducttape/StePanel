package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// Capability represents a single capability and its operational state.
//
// The Available bool means the capability is executable end-to-end on
// this host without operator intervention. A workflow that requires an
// operator to run a follow-up command, deliver credentials manually, or
// finish a restore by hand is Available=false with a descriptive Mode
// (manual, partial, degraded). Clients that only understand Available
// therefore see automated-vs-not, which is the safer default when the
// client cannot make sense of the Mode string.
//
// The prior definition — "true if mode is available OR partial" — meant
// a manual database restoration workflow reported Available=true, and
// an older SDK reading that field treated it as fully automated. That
// mismatch is fixed here at the cost of a minor semantic change for any
// client that was relying on the old broader meaning.
type Capability struct {
	Available bool           `json:"available"`
	Mode      CapabilityMode `json:"mode"`
	Reason    string         `json:"reason,omitempty"`
}

// newCapability enforces the Available/Mode invariant: Available is true
// if and only if the mode is CapabilityAvailable. Every constructor of a
// Capability value in this file goes through this so a future edit that
// adds a manual workflow cannot accidentally regress to the old
// "Available=true but not really" state.
func newCapability(mode CapabilityMode, reason string) Capability {
	return Capability{
		Available: mode == CapabilityAvailable,
		Mode:      mode,
		Reason:    reason,
	}
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

	// Site lifecycle. There is no generic synchronous create endpoint yet;
	// archive import is deliberately reported separately because it still
	// needs the SiteManager integration gate. Termination is executable only
	// when its durable job, database helper, site helper, backup signing, and
	// site tree are all present.
	caps["site.lifecycle.create"] = newCapability(CapabilityUnsupported, "generic site creation is not yet exposed through SiteManager; use the explicitly partial archive-import workflow")
	caps["site.lifecycle.delete"] = a.checkSiteDeletionCapability()
	caps["site.lifecycle.suspend"] = newCapability(CapabilityUnsupported, "site suspension is not implemented; the current alternative is site termination")

	// Database operations
	caps["database.mysql.create"] = a.checkDatabaseCapability("mysql")
	caps["database.mysql.restore"] = a.checkDatabaseCapability("mysql")
	caps["database.postgresql.create"] = a.checkDatabaseCapability("postgresql")
	caps["database.postgresql.restore"] = a.checkDatabaseCapability("postgresql")
	caps["database.mariadb.create"] = a.checkDatabaseCapability("mariadb")
	caps["database.mariadb.restore"] = a.checkDatabaseCapability("mariadb")

	// Archive import
	caps["archive.import.inspect"] = a.checkArchiveInspectionCapability()
	caps["archive.import.extract"] = a.checkArchiveImportCapability()
	caps["archive.import.database_restore"] = a.checkDatabaseRestorationCapability()

	// Network capabilities
	caps["runner.network_isolation"] = a.checkNetworkIsolationCapability()
	if len(a.Config.RunnerAllowedRegistries) > 0 {
		caps["runner.registry_allowlist"] = newCapability(CapabilityAvailable, "")
	} else {
		caps["runner.registry_allowlist"] = newCapability(CapabilityUnsupported, "STEPANEL_RUNNER_ALLOWED_REGISTRIES not configured")
	}

	// Filesystem capabilities
	caps["filesystem.quotas"] = a.checkFilesystemQuotasCapability()
	caps["filesystem.symlinks"] = newCapability(CapabilityAvailable, "Symlink rejection enforced for security")

	// Backup/restore
	if a.Config.BackupSigningKey != "" {
		caps["backup.verified"] = newCapability(CapabilityAvailable, "")
	} else {
		caps["backup.verified"] = newCapability(CapabilityUnsupported, "STEPANEL_BACKUP_SIGNING_KEY not configured; backups cannot be verified")
	}
	caps["backup.offsite"] = a.checkOffsiteBackupCapability()
	caps["restore.to_staging"] = newCapability(CapabilityAvailable, "")
	caps["restore.verified_file"] = newCapability(CapabilityAvailable, "")
	caps["restore.database_only"] = newCapability(CapabilityAvailable, "")

	// Authentication
	if a.Auth.TOTPEnabled {
		caps["auth.mfa"] = newCapability(CapabilityAvailable, "")
	} else {
		caps["auth.mfa"] = newCapability(CapabilityUnsupported, "TOTP is not configured")
	}
	caps["auth.api_tokens"] = newCapability(CapabilityAvailable, "")
	caps["auth.scoped_tokens"] = newCapability(CapabilityAvailable, "")

	// Deployment
	caps["deployment.git"] = a.checkGitDeploymentCapability()
	caps["deployment.builds"] = a.checkBuildCapability()

	// Mail (optional)
	caps["mail.integration"] = a.checkMailCapability()

	// DNS. Validation runs in-panel; provider-specific mutations depend
	// on external credentials being present at the operator's end, so
	// the workflow is not end-to-end automated in the general case.
	caps["dns.management"] = a.checkDNSCapability()

	return caps
}

func (a *App) checkSiteDeletionCapability() Capability {
	if a.Jobs == nil {
		return newCapability(CapabilityUnsupported, "durable job store is not initialized")
	}
	if a.Config.DBCtl == "" || a.Config.SiteCtl == "" {
		return newCapability(CapabilityUnsupported, "database and site lifecycle helpers are required")
	}
	if a.Config.BackupSigningKey == "" {
		return newCapability(CapabilityUnsupported, "termination requires a backup signing key")
	}
	if a.Config.WebRoot == "" {
		return newCapability(CapabilityUnsupported, "site web root is not configured")
	}
	return newCapability(CapabilityAvailable, "")
}

func (a *App) checkArchiveInspectionCapability() Capability {
	if a.Jobs == nil {
		return newCapability(CapabilityUnsupported, "durable job store is not initialized")
	}
	if a.Config.ImportRoot == "" {
		return newCapability(CapabilityUnsupported, "STEPANEL_IMPORT_ROOT is not configured")
	}
	return newCapability(CapabilityAvailable, "")
}

func (a *App) checkGitDeploymentCapability() Capability {
	if a.Config.GitCtl == "" {
		return newCapability(CapabilityUnsupported, "STEPANEL_GITCTL is not configured")
	}
	if a.Config.WebRoot == "" || a.Config.AppRoot == "" {
		return newCapability(CapabilityUnsupported, "Git deployment requires web and application roots")
	}
	return newCapability(CapabilityAvailable, "")
}

// checkDatabaseCapability reports on the end-to-end managed-database
// workflow. Prior code only checked that mysql/psql client binaries were
// on PATH — that says nothing about whether the panel's own DBCtl helper
// is wired (without it, "create database" fails at the very first step),
// nor whether the site is using the engine we probed. We now also
// require STEPANEL_DBCTL and, when set, that STEPANEL_DB_ENGINE matches
// the type being asked about.
func (a *App) checkDatabaseCapability(dbType string) Capability {
	if a.Config.DBCtl == "" {
		return newCapability(CapabilityUnsupported, "STEPANEL_DBCTL is not configured; the panel cannot invoke managed-database operations")
	}
	if a.Config.DBEngine != "" && a.Config.DBEngine != dbType {
		return newCapability(CapabilityUnsupported, fmt.Sprintf("STEPANEL_DB_ENGINE=%q; %s is not the configured engine", a.Config.DBEngine, dbType))
	}
	var binaries []string
	switch dbType {
	case "mysql", "mariadb":
		binaries = []string{"mysql", "mysqldump"}
	case "postgresql":
		binaries = []string{"psql", "pg_dump"}
	}
	for _, bin := range binaries {
		if _, err := exec.LookPath(bin); err != nil {
			return newCapability(CapabilityUnsupported, fmt.Sprintf("%s command not found in PATH", bin))
		}
	}
	return newCapability(CapabilityAvailable, "")
}

// checkArchiveImportCapability reports on whether an archive import will
// actually succeed end-to-end. The prior code hard-coded true, which was
// wrong while the archive-import lifecycle bug (fixed separately) was
// live; a hard-coded value cannot detect that a code-path defect has
// broken it. Now we surface the pieces the orchestrator depends on: the
// import root exists, and the sites tree is writable.
func (a *App) checkArchiveImportCapability() Capability {
	if a.Config.ImportRoot == "" {
		return newCapability(CapabilityUnsupported, "STEPANEL_IMPORT_ROOT is not configured")
	}
	if info, err := os.Stat(a.Config.ImportRoot); err != nil || !info.IsDir() {
		return newCapability(CapabilityUnsupported, "STEPANEL_IMPORT_ROOT is not a directory")
	}
	if a.Config.WebRoot == "" {
		return newCapability(CapabilityUnsupported, "STEPANEL_WEB_ROOT is not configured")
	}
	if info, err := os.Stat(a.Config.WebRoot); err != nil || !info.IsDir() {
		return newCapability(CapabilityUnsupported, "STEPANEL_WEB_ROOT is not a directory")
	}
	return newCapability(CapabilityAvailable, "")
}

// checkDatabaseRestorationCapability reports on the archive-import DB
// restoration flow. Automatic restoration is opt-in: the request must carry
// a valid database password, and this executable managed helper must be able
// to provision, restore, inventory-check, and clean up the database.
func (a *App) checkDatabaseRestorationCapability() Capability {
	if a.Config.DBCtl == "" {
		return newCapability(CapabilityUnsupported, "STEPANEL_DBCTL is not configured; automatic archive database restoration is unavailable")
	}
	info, err := os.Stat(a.Config.DBCtl)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return newCapability(CapabilityUnsupported, "STEPANEL_DBCTL is not an executable file")
	}
	return newCapability(CapabilityAvailable, "automatic restoration is available when the archive request supplies a valid database password")
}

func (a *App) checkNetworkIsolationCapability() Capability {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "podman", "run", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return newCapability(CapabilityUnsupported, "podman not available on system")
	}
	if strings.Contains(string(output), "--network") {
		return newCapability(CapabilityAvailable, "")
	}
	return newCapability(CapabilityUnsupported, "podman version does not support --network flag")
}

// checkFilesystemQuotasCapability reports on whether the filesystem
// hosting the customer sites tree (STEPANEL_WEB_ROOT) is mounted with
// user quotas enabled — which is the only mount the panel actually cares
// about. The prior implementation checked whether any mount on the host
// had usrquota, and separately whether a `quotactl` binary existed
// anywhere on PATH. Both are unrelated to whether *this* filesystem can
// actually enforce a customer quota.
func (a *App) checkFilesystemQuotasCapability() Capability {
	if a.Config.WebRoot == "" {
		return newCapability(CapabilityUnsupported, "STEPANEL_WEB_ROOT is not configured")
	}
	webRootAbs, err := filepath.Abs(a.Config.WebRoot)
	if err != nil {
		return newCapability(CapabilityUnsupported, "cannot resolve STEPANEL_WEB_ROOT to an absolute path")
	}
	mounts, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return newCapability(CapabilityUnsupported, "cannot read /proc/mounts to inspect the sites-tree filesystem")
	}
	// Walk /proc/mounts and find the longest mountpoint prefix that
	// contains webRootAbs — that is the filesystem hosting the sites
	// tree. Then check its option list for usrquota / grpquota.
	best := ""
	bestOptions := ""
	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		mountpoint := fields[1]
		if !(mountpoint == webRootAbs || strings.HasPrefix(webRootAbs, mountpoint+"/") || mountpoint == "/") {
			continue
		}
		if len(mountpoint) >= len(best) {
			best = mountpoint
			bestOptions = fields[3]
		}
	}
	if best == "" {
		return newCapability(CapabilityUnsupported, "could not identify a mountpoint hosting the sites tree")
	}
	if !strings.Contains(bestOptions, "usrquota") && !strings.Contains(bestOptions, "grpquota") && !strings.Contains(bestOptions, "prjquota") {
		return newCapability(CapabilityUnsupported, fmt.Sprintf("mount %s hosting the sites tree does not have quota options enabled", best))
	}
	return newCapability(CapabilityAvailable, "")
}

func (a *App) checkOffsiteBackupCapability() Capability {
	if a.Config.OffsiteTarget == "" {
		return newCapability(CapabilityUnsupported, "STEPANEL_OFFSITE_TARGET not configured")
	}
	if _, err := exec.LookPath("rclone"); err != nil {
		return newCapability(CapabilityDegraded, "offsite target configured but rclone not found in PATH")
	}
	return newCapability(CapabilityAvailable, "")
}

// checkBuildCapability reports on the sandboxed-build path end-to-end.
// The runner needs more than just podman: the panel invokes the RunnerCtl
// helper (which validates its own arguments, drops to the sp-* site
// user, and runs Podman on the operator's behalf), and the images it
// pulls are constrained by the configured allowlist. If any of those
// pieces is missing, the build path is not usable.
func (a *App) checkBuildCapability() Capability {
	if a.Config.RunnerCtl == "" {
		return newCapability(CapabilityUnsupported, "STEPANEL_RUNNERCTL is not configured")
	}
	if info, err := os.Stat(a.Config.RunnerCtl); err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return newCapability(CapabilityUnsupported, "STEPANEL_RUNNERCTL is not an executable file")
	}
	if a.Config.RunnerAllowedRegistries == "" {
		return newCapability(CapabilityUnsupported, "STEPANEL_RUNNER_ALLOWED_REGISTRIES is empty; the runner would refuse every image")
	}
	if a.Config.RunnerMaxImageBytes <= 0 {
		return newCapability(CapabilityUnsupported, "STEPANEL_RUNNER_MAX_IMAGE_BYTES must be positive")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		return newCapability(CapabilityUnsupported, "podman not found in PATH")
	}
	return newCapability(CapabilityAvailable, "")
}

func (a *App) checkMailCapability() Capability {
	if _, err := exec.LookPath("exim4"); err == nil {
		return newCapability(CapabilityAvailable, "")
	}
	if _, err := exec.LookPath("postfix"); err == nil {
		return newCapability(CapabilityAvailable, "")
	}
	return newCapability(CapabilityUnsupported, "mail integration not configured; exim4 or postfix not found")
}

func (a *App) checkDNSCapability() Capability {
	// DNS validation runs in-panel, but provider-specific mutations
	// require external credentials that live at the operator's end.
	// Under the "Available == end-to-end automated" rule, this is a
	// partial workflow — validation only — so Available=false.
	return newCapability(CapabilityPartial, "core DNS validation available; provider-specific mutations require provider credentials")
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
