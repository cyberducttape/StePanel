package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Build images must be immutable references so the same request cannot run
// different code after a registry tag is moved.
var runnerImagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,180}@sha256:[0-9a-f]{64}$`)

// extractRegistry extracts the registry hostname from an image reference
// Format: registry.com/org/image@sha256:...
func extractRegistry(image string) string {
	// Remove digest
	parts := strings.Split(image, "@")
	if len(parts) > 0 {
		image = parts[0]
	}
	// Get first component (registry)
	parts = strings.Split(image, "/")
	if len(parts) > 0 {
		return strings.ToLower(parts[0])
	}
	return ""
}

// registryAllowed checks if a registry is in the allowlist
func registryAllowed(registry string, allowedList string) bool {
	if allowedList == "" {
		return false // Explicit denylist by default
	}
	allowed := strings.Split(allowedList, ",")
	registryLower := strings.ToLower(strings.TrimSpace(registry))
	for _, a := range allowed {
		if strings.ToLower(strings.TrimSpace(a)) == registryLower {
			return true
		}
	}
	return false
}

type BuildRequest struct {
	Site     string   `json:"site"`
	Image    string   `json:"image"`
	Commands []string `json:"commands"`
}

// runnerBuild executes a sandboxed build operation with security constraints:
//
// Security controls:
// - Registry allowlist: only images from STEPANEL_RUNNER_ALLOWED_REGISTRIES (default: docker.io,ghcr.io,quay.io)
// - Digest pinning: image must be referenced by SHA256 digest (prevents tag mutation)
// - Network disabled by default: STEPANEL_RUNNER_NETWORK_ENABLED controls egress
// - Resource limits: CPU %, memory MB, task count per site plan
// - Image size limit: STEPANEL_MAX_IMAGE_SIZE prevents decompression bombs (default: 5GB)
// - Rootless Podman: reduces kernel attack surface
//
// TODO: Enhanced constraints for production:
// - Explicit seccomp profile
// - SELinux/AppArmor context
// - Storage quota for image pulls
// - Egress restrictions (only allow build-network when explicitly enabled)
// - Cleanup verification after build
// - No host socket exposure
func (a *App) runnerBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var input BuildRequest
	if err := decodeJSON(w, r, 32<<10, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input.Site = safeUser(input.Site)
	input.Image = strings.TrimSpace(input.Image)
	if input.Site == "" || !runnerImagePattern.MatchString(input.Image) || len(input.Commands) == 0 || len(input.Commands) > 16 {
		http.Error(w, "invalid build definition", 422)
		return
	}

	// SECURITY: Validate registry is allowed
	registry := extractRegistry(input.Image)
	if registry == "" {
		http.Error(w, "could not extract registry from image", 422)
		return
	}
	if !registryAllowed(registry, a.Config.RunnerAllowedRegistries) {
		http.Error(w, fmt.Sprintf("registry %q is not in allowed list", registry), 403)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, input.Site, "site is not assigned to this account", 403); !ok {
		return
	}
	releaseUnlock := a.siteOperations.Acquire(input.Site)
	defer releaseUnlock()
	for _, line := range input.Commands {
		if len(line) == 0 || len(line) > 1024 || strings.ContainsAny(line, "\x00\r\n") {
			http.Error(w, "invalid build command", 422)
			return
		}
	}
	root := filepath.Join(a.Config.WebRoot, "sites", input.Site, "public")
	if _, err := os.Stat(root); err != nil {
		http.Error(w, "site document root does not exist", 422)
		return
	}
	script, err := os.CreateTemp(a.Config.AppRoot, "runner-*.sh")
	if err != nil {
		http.Error(w, "create runner definition", 500)
		return
	}
	scriptPath := script.Name()
	defer os.Remove(scriptPath)
	if _, err = script.WriteString("set -eu\n" + strings.Join(input.Commands, "\n") + "\n"); err == nil {
		err = script.Close()
	}
	if err != nil {
		http.Error(w, "write runner definition", 500)
		return
	}
	if err = os.Chmod(scriptPath, 0600); err != nil {
		http.Error(w, "secure runner definition", 500)
		return
	}
	cpuPercent, memoryMB, tasksMax := a.pipelineResourceLimits(input.Site)

	// SECURITY: Pass runtime constraints to runner helper
	// Network is disabled by default unless explicitly enabled
	networkFlag := "0"
	if a.Config.RunnerNetworkEnabled {
		networkFlag = "1"
	}
	maxImageSizeStr := strconv.FormatInt(a.Config.MaxImageSize, 10)

	if err = runHelperCommand(r.Context(), a.Config, a.Config.RunnerCtl, "build", input.Site, input.Image, root, scriptPath, strconv.Itoa(cpuPercent), strconv.Itoa(memoryMB), strconv.Itoa(tasksMax), networkFlag, maxImageSizeStr); err != nil {
		http.Error(w, "sandboxed build failed", 502)
		return
	}
	_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "runner.build", input.Site, input.Image)
	a.recordDeployment(input.Site, "build", "completed", input.Image, gitDeployResult{}, filepath.Join(a.Config.WebRoot, "sites", input.Site, ".stepanel-artifact"))
	writeJSON(w, 202, map[string]any{"site": input.Site, "image": input.Image, "commands": len(input.Commands), "artifact": filepath.Join(a.Config.WebRoot, "sites", input.Site, ".stepanel-artifact")})
}
