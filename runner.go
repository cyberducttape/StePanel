package stepanel

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

// Build images must be immutable references so the same request cannot run
// different code after a registry tag is moved.
var runnerImagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,180}@sha256:[0-9a-f]{64}$`)

type BuildRequest struct {
	Site     string   `json:"site"`
	Image    string   `json:"image"`
	Commands []string `json:"commands"`
}

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
	// Validate container image against registry allowlist and size limits from config
	allowed := make(map[string]bool)
	for _, registry := range strings.Split(a.Config.RunnerAllowedRegistries, ",") {
		allowed[strings.TrimSpace(registry)] = true
	}
	ref, err := ValidateContainerImageForSite(input.Image, allowed)
	if err != nil {
		http.Error(w, "container image not allowed: "+err.Error(), 403)
		return
	}
	// Narrow-allowlist check: if STEPANEL_RUNNER_ALLOWED_IMAGES is set,
	// the image must also match a configured namespace/repo pattern.
	// Empty configuration means no additional constraint.
	if patterns := ParseImagePatternList(a.Config.RunnerAllowedImages); len(patterns) > 0 && !ImageAllowedByPatterns(ref, patterns) {
		http.Error(w, "container image not in the configured STEPANEL_RUNNER_ALLOWED_IMAGES allowlist", 403)
		return
	}

	// The privileged runner helper pulls the pinned digest and enforces the
	// configured byte ceiling immediately before execution.
	if _, ok := a.requireSiteAccess(w, r, input.Site, "site is not assigned to this account", 403); !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for build operations", http.StatusForbidden)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "site build is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	for _, line := range input.Commands {
		if len(line) == 0 || len(line) > 1024 || strings.ContainsAny(line, "\x00\r\n") {
			http.Error(w, "invalid build command", 422)
			return
		}
	}
	root, err := safePath(a.Config.WebRoot, "sites", input.Site, "public")
	if err != nil {
		http.Error(w, "invalid site root", 422)
		return
	}
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
	args := a.pipelineBuildArgs(input.Site, input.Image, root, scriptPath)
	request, parseErr := runnerRequestFromArgs(args)
	if parseErr != nil {
		http.Error(w, "invalid runner resource limits", http.StatusInternalServerError)
		return
	}
	if err = runRunnerBuild(operationCtx, a.Config, request); err != nil {
		http.Error(w, "sandboxed build failed", 502)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "sandboxed build cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "runner.build", input.Site, input.Image)
	artifact, err := safePath(a.Config.WebRoot, "sites", input.Site, ".stepanel-artifact")
	if err != nil {
		http.Error(w, "invalid build artifact path", 500)
		return
	}
	response := map[string]any{"site": input.Site, "image": input.Image, "commands": len(input.Commands), "artifact": artifact}
	if err := a.recordDeployment(input.Site, "build", "completed", input.Image, gitDeployResult{}, artifact); err != nil {
		response["history_error"] = "build completed but deployment history was not persisted: " + err.Error()
	}
	writeJSON(w, 202, response)
}

func runnerRequestFromArgs(args []string) (rootbroker.RunnerRequest, error) {
	if len(args) != 10 || args[0] != "build" {
		return rootbroker.RunnerRequest{}, errors.New("invalid runner arguments")
	}
	cpu, err := strconv.Atoi(args[5])
	if err != nil {
		return rootbroker.RunnerRequest{}, err
	}
	memory, err := strconv.Atoi(args[6])
	if err != nil {
		return rootbroker.RunnerRequest{}, err
	}
	tasks, err := strconv.Atoi(args[7])
	if err != nil {
		return rootbroker.RunnerRequest{}, err
	}
	maxBytes, err := strconv.ParseInt(args[9], 10, 64)
	if err != nil {
		return rootbroker.RunnerRequest{}, err
	}
	return rootbroker.RunnerRequest{Action: "build", Site: args[1], Image: args[2], Root: args[3], Script: args[4], CPUPercent: cpu, MemoryMB: memory, TasksMax: tasks, NetworkMode: args[8], MaxImageBytes: maxBytes}, nil
}
