package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func decodeBuildRequest(body []byte) (BuildRequest, error) {
	if len(body) > 32<<10 {
		return BuildRequest{}, errors.New("build request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input BuildRequest
	if err := decoder.Decode(&input); err != nil {
		return BuildRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return BuildRequest{}, errors.New("request body must contain one JSON value")
	}
	input.Site = safeUser(input.Site)
	input.Image = strings.TrimSpace(input.Image)
	if input.Site == "" || !runnerImagePattern.MatchString(input.Image) || len(input.Commands) == 0 || len(input.Commands) > 16 {
		return BuildRequest{}, errors.New("invalid build definition")
	}
	for _, line := range input.Commands {
		if len(line) == 0 || len(line) > 1024 || strings.ContainsAny(line, "\x00\r\n") {
			return BuildRequest{}, errors.New("invalid build command")
		}
	}
	return input, nil
}

func validateRunnerBuildPayload(body []byte) error {
	_, err := decodeBuildRequest(body)
	return err
}

type runnerBuildFailure struct {
	status  int
	message string
}

func (e *runnerBuildFailure) Error() string { return e.message }

func runnerBuildStatus(err error) int {
	var failure *runnerBuildFailure
	if errors.As(err, &failure) {
		return failure.status
	}
	return http.StatusBadGateway
}

func (a *App) runnerBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input, err := decodeBuildRequest(body)
	if err != nil {
		http.Error(w, "invalid build definition", http.StatusUnprocessableEntity)
		return
	}
	access, ok := a.requireSiteAccess(w, r, input.Site, "site is not assigned to this account", 403)
	if !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for build operations", http.StatusForbidden)
		return
	}
	response, err := a.applyRunnerBuild(r.Context(), a.Auth.AuditActor(r), access, input)
	if err != nil {
		http.Error(w, err.Error(), runnerBuildStatus(err))
		return
	}
	writeJSON(w, http.StatusAccepted, response)
}

func executeRunnerBuildCommand(a *App, ctx context.Context, command siteOperationCommand, principal siteOperationPrincipal) ([]byte, error) {
	input, err := decodeBuildRequest(command.Body)
	if err != nil || input.Site != command.Site {
		return nil, errors.New("invalid runner build command")
	}
	access, err := a.authorizeDurableSiteJob(input.Site, principal.Actor, false)
	if err != nil {
		return nil, fmt.Errorf("runner build authorization: %w", err)
	}
	if !a.durableSiteOperationScopeAllowed(principal, "site:deploy") {
		return nil, errors.New("runner build authorization: required site:deploy permission is no longer active")
	}
	response, err := a.applyRunnerBuild(ctx, principal.Actor, access, input)
	if err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

func (a *App) applyRunnerBuild(ctx context.Context, actor string, access SiteCapability, input BuildRequest) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, &runnerBuildFailure{http.StatusConflict, "sandboxed build was cancelled before it started"}
	}
	allowed := make(map[string]bool)
	for _, registry := range strings.Split(a.Config.RunnerAllowedRegistries, ",") {
		if registry = strings.TrimSpace(registry); registry != "" {
			allowed[registry] = true
		}
	}
	ref, err := ValidateContainerImageForSite(input.Image, allowed)
	if err != nil {
		return nil, &runnerBuildFailure{http.StatusForbidden, "container image not allowed: " + err.Error()}
	}
	if patterns := ParseImagePatternList(a.Config.RunnerAllowedImages); len(patterns) > 0 && !ImageAllowedByPatterns(ref, patterns) {
		return nil, &runnerBuildFailure{http.StatusForbidden, "container image is not in the configured runner allowlist"}
	}
	operationCtx, release, err := a.acquireSiteMutationLockContext(ctx, access.Site())
	if err != nil {
		return nil, &runnerBuildFailure{http.StatusConflict, "site build is busy"}
	}
	defer release()
	if err := operationCtx.Err(); err != nil {
		return nil, &runnerBuildFailure{http.StatusConflict, "sandboxed build cancelled because the mutation lock was lost"}
	}
	root, err := siteDocRoot(a.Config, access.Site())
	if err != nil {
		return nil, &runnerBuildFailure{http.StatusUnprocessableEntity, "invalid site root"}
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, &runnerBuildFailure{http.StatusUnprocessableEntity, "site document root does not exist"}
	}
	script, err := os.CreateTemp(a.Config.AppRoot, "runner-*.sh")
	if err != nil {
		return nil, &runnerBuildFailure{http.StatusInternalServerError, "could not create runner definition"}
	}
	scriptPath := script.Name()
	defer os.Remove(scriptPath)
	_, writeErr := script.WriteString("set -eu\n" + strings.Join(input.Commands, "\n") + "\n")
	closeErr := script.Close()
	if writeErr != nil || closeErr != nil {
		return nil, &runnerBuildFailure{http.StatusInternalServerError, "could not write runner definition"}
	}
	if err := os.Chmod(scriptPath, 0600); err != nil {
		return nil, &runnerBuildFailure{http.StatusInternalServerError, "could not secure runner definition"}
	}
	args := a.pipelineBuildArgs(access.Site(), input.Image, root, scriptPath)
	request, err := runnerRequestFromArgs(args)
	if err != nil {
		return nil, &runnerBuildFailure{http.StatusInternalServerError, "runner resource limits are invalid"}
	}
	if err := runRunnerBuild(operationCtx, a.Config, request); err != nil {
		return nil, &runnerBuildFailure{http.StatusBadGateway, "sandboxed build failed"}
	}
	if err := operationCtx.Err(); err != nil {
		return nil, &runnerBuildFailure{http.StatusConflict, "sandboxed build cancelled because the mutation lock was lost"}
	}
	TelemetryAudit(a.Config.AuditLog, actor, "runner.build", access.Site(), input.Image)
	artifact, err := sitePrivatePath(a.Config, access.Site(), ".stepanel-artifact")
	if err != nil {
		return nil, &runnerBuildFailure{http.StatusInternalServerError, "invalid build artifact path"}
	}
	response := map[string]any{"site": access.Site(), "image": input.Image, "commands": len(input.Commands), "artifact": artifact}
	if err := a.recordDeployment(access.Site(), "build", "completed", input.Image, gitDeployResult{}, artifact); err != nil {
		response["history_error"] = "build completed but deployment history was not persisted: " + err.Error()
	}
	return response, nil
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
