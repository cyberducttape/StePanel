package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ReleasePipelineRequest accepts a declarative build which must write the
// complete deployable tree to /artifact. The source checkout is read-only in
// the runner, so generated files cannot alter it before validation.
type ReleasePipelineRequest struct {
	Site       string   `json:"site"`
	Repository string   `json:"repository"`
	Ref        string   `json:"ref"`
	Image      string   `json:"image"`
	Commands   []string `json:"commands"`
	Backup     bool     `json:"backup_before_activate"`
}

func (a *App) releasePipeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var input ReleasePipelineRequest
	if err := decodeJSON(w, r, 48<<10, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input.Site = safeUser(input.Site)
	input.Repository = strings.TrimSpace(input.Repository)
	input.Ref = strings.TrimSpace(input.Ref)
	input.Image = strings.TrimSpace(input.Image)
	if input.Ref == "" {
		input.Ref = "main"
	}
	if input.Site == "" {
		http.Error(w, "invalid release pipeline", 422)
		return
	}
	access, ok := a.requireSiteAccess(w, r, input.Site, "invalid release pipeline", 422)
	if !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for release pipelines", http.StatusForbidden)
		return
	}
	if !gitRefPattern.MatchString(input.Ref) || !runnerImagePattern.MatchString(input.Image) || len(input.Commands) == 0 || len(input.Commands) > 16 {
		http.Error(w, "invalid release pipeline", 422)
		return
	}
	// The direct /api/runner/build path enforces the registry allowlist
	// and (when configured) the narrow image allowlist; the release
	// pipeline used to accept the pinned-digest regex alone, which let it
	// pull from any registry the runnerImagePattern happened to permit.
	// Apply the same two checks here so the two build paths cannot diverge.
	allowed := map[string]bool{}
	for _, registry := range strings.Split(a.Config.RunnerAllowedRegistries, ",") {
		allowed[strings.TrimSpace(registry)] = true
	}
	imageRef, err := ValidateContainerImageForSite(input.Image, allowed)
	if err != nil {
		http.Error(w, "container image not allowed: "+err.Error(), 403)
		return
	}
	if patterns := ParseImagePatternList(a.Config.RunnerAllowedImages); len(patterns) > 0 && !ImageAllowedByPatterns(imageRef, patterns) {
		http.Error(w, "container image not in the configured STEPANEL_RUNNER_ALLOWED_IMAGES allowlist", 403)
		return
	}
	for _, line := range input.Commands {
		if len(line) == 0 || len(line) > 1024 || strings.ContainsAny(line, "\x00\r\n") {
			http.Error(w, "invalid build command", 422)
			return
		}
	}
	repository, err := parseGitRepository(input.Repository, a.Config.GitAllowedHosts)
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	siteRoot, err := existingManagedSiteRoot(a.Config.WebRoot, input.Site)
	if err != nil {
		http.Error(w, "invalid site root", 422)
		return
	}
	if _, err := safePath(a.Config.WebRoot, "sites", input.Site, "public"); err != nil {
		http.Error(w, "invalid site root", 422)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "site is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	ctx, cancel := context.WithTimeout(operationCtx, 20*time.Minute)
	defer cancel()
	deploymentID, err := newJobID("deployment")
	if err != nil {
		http.Error(w, "could not create deployment identity", http.StatusInternalServerError)
		return
	}
	result := gitDeployResult{DeploymentID: deploymentID, Site: input.Site, Repository: input.Repository, Ref: input.Ref}
	a.recordDeployment(input.Site, "checkout", "running", "pipeline checkout started", result, "")
	release, commit, err := a.checkoutPipelineRelease(ctx, access, repository, input.Ref)
	if err != nil {
		a.recordDeployment(input.Site, "checkout", "failed", err.Error(), result, "")
		http.Error(w, "Git checkout failed", 502)
		return
	}
	result.Commit = commit
	defer func() { _ = a.discardSiteReleaseStaging(context.Background(), input.Site, release) }()
	if input.Backup {
		a.recordDeployment(input.Site, "backup", "running", "pre-activation backup started", result, "")
		if _, err := CreateSiteBackup(a.Config, access, true); err != nil {
			a.recordDeployment(input.Site, "backup", "failed", err.Error(), result, "")
			http.Error(w, "pre-activation backup failed", 502)
			return
		}
		a.recordDeployment(input.Site, "backup", "completed", "verified pre-activation backup", result, "")
	}
	if err := a.runPipelineBuild(ctx, input.Site, input.Image, input.Commands, release); err != nil {
		a.recordDeployment(input.Site, "build", "failed", err.Error(), result, "")
		http.Error(w, "sandboxed build failed", 502)
		return
	}
	artifact := filepath.Join(siteRoot, ".stepanel-artifact")
	if err := validateGitRelease(artifact, a.Config.MaxEntries); err != nil {
		a.recordDeployment(input.Site, "build", "failed", "artifact unsafe: "+err.Error(), result, artifact)
		http.Error(w, "build artifact is unsafe", 422)
		return
	}
	if err := a.discardSiteReleaseStaging(operationCtx, input.Site, release); err != nil {
		http.Error(w, "could not replace source with artifact", 500)
		return
	}
	if err := os.Rename(artifact, release); err != nil {
		http.Error(w, "could not stage build artifact", 500)
		return
	}
	a.recordDeployment(input.Site, "build", "completed", "validated sandbox artifact", result, release)
	previous, err := a.activatePipelineRelease(operationCtx, input.Site, release)
	if err != nil {
		a.recordDeployment(input.Site, "activation", "failed", err.Error(), result, "")
		http.Error(w, "atomic activation failed", 503)
		return
	}
	result.Previous = previous
	a.recordDeployment(input.Site, "activation", "completed", "atomic built release activated", result, "")

	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.release.pipeline", input.Site, input.Repository+"@"+commit)
	writeJSON(w, 202, result)
}

func (a *App) checkoutPipelineRelease(ctx context.Context, site SiteCapability, repository gitRepository, ref string) (string, string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", "", err
	}
	release, err := a.createSiteReleaseStaging(ctx, site.Site(), ".stepanel-release-")
	if err != nil {
		return "", "", err
	}
	var output []byte
	if repository.Private {
		output, err = runBoundedCommand(ctx, helperCommandContext(ctx, a.Config, a.Config.GitCtl, "clone", site.Site(), repository.URL, ref, release, a.Config.GitAllowedHosts))
	} else {
		cmd := exec.CommandContext(ctx, gitPath, "-c", "credential.helper=", "clone", "--depth", "1", "--branch", ref, "--single-branch", "--no-tags", repository.URL, release)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		output, err = runBoundedCommand(ctx, cmd)
	}
	if err != nil {
		_ = a.discardSiteReleaseStaging(context.Background(), site.Site(), release)
		return "", "", err
	}
	commitOutput, err := runBoundedCommand(ctx, exec.CommandContext(ctx, gitPath, "-C", release, "rev-parse", "HEAD"))
	if err != nil {
		_ = a.discardSiteReleaseStaging(context.Background(), site.Site(), release)
		return "", "", err
	}
	commit := strings.TrimSpace(string(commitOutput))
	if !gitCommitPattern.MatchString(commit) || validateGitRelease(release, a.Config.MaxEntries) != nil || os.RemoveAll(filepath.Join(release, ".git")) != nil {
		_ = a.discardSiteReleaseStaging(context.Background(), site.Site(), release)
		return "", "", fmt.Errorf("invalid checked-out release: %s", strings.TrimSpace(string(output)))
	}
	return release, commit, nil
}

func (a *App) runPipelineBuild(ctx context.Context, site, image string, commands []string, root string) error {
	script, err := os.CreateTemp(a.Config.AppRoot, "pipeline-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(script.Name())
	if _, err = script.WriteString("set -eu\n" + strings.Join(commands, "\n") + "\n"); err == nil {
		err = script.Close()
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(script.Name(), 0600); err != nil {
		return err
	}
	args := a.pipelineBuildArgs(site, image, root, script.Name())
	return runHelperCommand(ctx, a.Config, a.Config.RunnerCtl, args...)
}

// pipelineBuildArgs constructs the argument list for the stepanel-runnerctl
// build command. It exists as a separate function so the number and order of
// arguments — which must match the helper's fixed positional signature at
// deploy/integrations/stepanel-runnerctl — can be asserted from a unit test
// without invoking the real helper.
func (a *App) pipelineBuildArgs(site, image, root, scriptPath string) []string {
	cpuPercent, memoryMB, tasksMax := a.pipelineResourceLimits(site)
	maxImageBytes := a.Config.RunnerMaxImageBytes
	if maxImageBytes <= 0 {
		maxImageBytes = defaultRunnerMaxImageBytes
	}
	return []string{
		"build",
		site,
		image,
		root,
		scriptPath,
		strconv.Itoa(cpuPercent),
		strconv.Itoa(memoryMB),
		strconv.Itoa(tasksMax),
		a.Config.RunnerNetworkMode,
		strconv.FormatInt(maxImageBytes, 10),
	}
}

func (a *App) pipelineResourceLimits(site string) (cpuPercent, memoryMB, tasksMax int) {
	// Keep builds bounded even for sites created before resource profiles were
	// introduced. A persisted site profile overrides these conservative
	// defaults and remains the source of truth for the application envelope.
	cpuPercent, memoryMB, tasksMax = 100, 512, 128
	if a.Resources == nil {
		return cpuPercent, memoryMB, tasksMax
	}
	a.Resources.mu.RLock()
	profile, ok := a.Resources.values[site]
	a.Resources.mu.RUnlock()
	if ok && validResourceProfile(profile) {
		return profile.CPUPercent, profile.MemoryMB, profile.TasksMax
	}
	return cpuPercent, memoryMB, tasksMax
}
func (a *App) activatePipelineRelease(ctx context.Context, site, release string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	journal, err := newReleaseActivationJournal(a.Config.RecoveryRoot, site, release)
	if err != nil {
		return "", err
	}
	if err := journal.persist(); err != nil {
		return "", fmt.Errorf("persist release activation journal: %w", err)
	}
	a.gitActivationMu.Lock()
	defer a.gitActivationMu.Unlock()
	if err := failureInjection("deploy", "activate"); err != nil {
		_ = journal.cleanup()
		return "", err
	}
	previous, err := a.activateReplacingSite(ctx, site, release)
	if err != nil {
		_ = journal.cleanup()
		return "", err
	}
	if err := journal.markActivated(previous); err != nil {
		if rollbackErr := a.rollbackReplacingSite(ctx, site, previous); rollbackErr != nil {
			return "", fmt.Errorf("persist release activation state: %w; rollback failed: %v", err, rollbackErr)
		}
		_ = journal.cleanup()
		return "", fmt.Errorf("persist release activation state: %w", err)
	}
	if err := siteHelperContext(ctx, a.Config, "seal", site); err != nil {
		if rollbackErr := a.rollbackReplacingSite(ctx, site, previous); rollbackErr != nil {
			return "", fmt.Errorf("seal site after release activation: %w; rollback failed: %v", err, rollbackErr)
		}
		_ = journal.cleanup()
		return "", err
	}
	if err := journal.markCommitted(); err != nil {
		return "", fmt.Errorf("commit release activation journal: %w", err)
	}
	if err := journal.cleanup(); err != nil {
		return "", err
	}
	return previous, nil
}
