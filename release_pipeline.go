package stepanel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
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
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "release cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "site.release.pipeline", input.Site, input.Repository+"@"+input.Ref)
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	var outcome string
	defer intent.Finish(&outcome, "pipeline did not activate a release")
	ctx, cancel := context.WithTimeout(operationCtx, 20*time.Minute)
	defer cancel()
	deploymentID, err := newJobID("deployment")
	if err != nil {
		http.Error(w, "could not create deployment identity", http.StatusInternalServerError)
		return
	}
	result := gitDeployResult{DeploymentID: deploymentID, Site: input.Site, Repository: input.Repository, Ref: input.Ref}
	if err := a.recordDeployment(input.Site, "checkout", "running", "pipeline checkout started", result, ""); err != nil {
		historyUnavailable(w, r)
		return
	}
	release, commit, err := a.checkoutPipelineRelease(ctx, access, repository, input.Ref)
	if err != nil {
		_ = a.recordDeployment(input.Site, "checkout", "failed", err.Error(), result, "") // failure already marks readiness
		http.Error(w, "Git checkout failed", 502)
		return
	}
	result.Commit = commit
	defer func() {
		logStagingDiscardFailure(release, a.discardSiteReleaseStaging(context.Background(), input.Site, release))
	}()
	if input.Backup {
		if err := a.recordDeployment(input.Site, "backup", "running", "pre-activation backup started", result, ""); err != nil {
			historyUnavailable(w, r)
			return
		}
		if _, err := createSiteBackupContext(operationCtx, a.Config, access, true, a.capacityLedger()); err != nil {
			_ = a.recordDeployment(input.Site, "backup", "failed", err.Error(), result, "") // failure already marks readiness
			http.Error(w, "pre-activation backup failed", 502)
			return
		}
		if err := a.recordDeployment(input.Site, "backup", "completed", "verified pre-activation backup", result, ""); err != nil {
			historyUnavailable(w, r)
			return
		}
	}
	if err := a.runPipelineBuild(ctx, input.Site, input.Image, input.Commands, release); err != nil {
		_ = a.recordDeployment(input.Site, "build", "failed", err.Error(), result, "") // failure already marks readiness
		http.Error(w, "sandboxed build failed", 502)
		return
	}
	artifact := filepath.Join(siteRoot, ".stepanel-artifact")
	if err := validateGitRelease(artifact, a.Config.MaxEntries); err != nil {
		_ = a.recordDeployment(input.Site, "build", "failed", "artifact unsafe: "+err.Error(), result, artifact) // failure already marks readiness
		http.Error(w, "build artifact is unsafe", 422)
		return
	}
	artifactBytes, err := gitReleaseTreeSize(artifact, a.Config.GitReleaseMaxTreeBytes)
	if err != nil {
		_ = a.recordDeployment(input.Site, "build", "failed", "artifact size limit exceeded", result, artifact) // failure already marks readiness
		http.Error(w, "build artifact exceeds the release size limit", 422)
		return
	}
	if err := a.discardSiteReleaseStaging(operationCtx, input.Site, release); err != nil {
		log.Printf("release pipeline checkout cleanup failed for site %s: %v", input.Site, err)
		_ = a.recordDeployment(input.Site, "build", "failed", "could not discard Git checkout staging: "+err.Error(), result, release)
		http.Error(w, "could not replace source with artifact", 500)
		return
	}
	// The runner artifact and Git checkout may be governed by different quota
	// identities. A rename can therefore fail even on the same filesystem. Copy
	// the validated artifact into a fresh manager-owned stage and reserve the
	// temporary duplicate space before writing it.
	release, err = a.createSiteReleaseStaging(operationCtx, input.Site, ".stepanel-release-")
	if err != nil {
		log.Printf("release pipeline staging directory creation failed for site %s: %v", input.Site, err)
		_ = a.recordDeployment(input.Site, "build", "failed", "could not create artifact staging directory: "+err.Error(), result, artifact)
		http.Error(w, "could not create release staging directory", 500)
		return
	}
	artifactReservation, err := a.capacityLedger().reserve(a.Config, "git-pipeline-artifact", release, []capacityDemand{{Path: release, Bytes: uint64(artifactBytes)}})
	if err != nil {
		log.Printf("release pipeline artifact admission failed for site %s: %v", input.Site, err)
		_ = a.recordDeployment(input.Site, "build", "failed", err.Error(), result, artifact)
		http.Error(w, "insufficient capacity to stage build artifact", http.StatusInsufficientStorage)
		return
	}
	defer artifactReservation.release()
	if err := copyGitArtifactTree(operationCtx, artifact, release, a.Config.GitReleaseMaxTreeBytes, artifactBytes); err != nil {
		log.Printf("release pipeline artifact copy failed for site %s: %v", input.Site, err)
		_ = a.recordDeployment(input.Site, "build", "failed", err.Error(), result, artifact)
		http.Error(w, "could not stage build artifact", 500)
		return
	}
	if err := runRunnerBuild(operationCtx, a.Config, rootbroker.RunnerRequest{Action: "artifact-cleanup", Site: input.Site}); err != nil {
		log.Printf("release pipeline artifact cleanup failed for site %s: %v", input.Site, err)
		_ = a.recordDeployment(input.Site, "build", "failed", err.Error(), result, artifact)
		http.Error(w, "could not clean up build artifact", 500)
		return
	}
	if err := ctx.Err(); err != nil {
		_ = a.recordDeployment(input.Site, "build", "failed", err.Error(), result, release)
		http.Error(w, "release cancelled before activation", http.StatusConflict)
		return
	}
	if _, err := os.Stat(release); err != nil {
		log.Printf("release pipeline staged artifact disappeared for site %s: %v", input.Site, err)
		_ = a.recordDeployment(input.Site, "build", "failed", "staged artifact disappeared: "+err.Error(), result, release)
		http.Error(w, "could not stage build artifact", 500)
		return
	}
	if err := validateGitRelease(release, a.Config.MaxEntries); err != nil {
		_ = a.recordDeployment(input.Site, "build", "failed", "staged artifact unsafe: "+err.Error(), result, release)
		http.Error(w, "staged build artifact is unsafe", 422)
		return
	}
	if _, err := gitReleaseTreeSize(release, a.Config.GitReleaseMaxTreeBytes); err != nil {
		_ = a.recordDeployment(input.Site, "build", "failed", "staged artifact exceeded size limit: "+err.Error(), result, release)
		http.Error(w, "could not stage build artifact", 500)
		return
	}
	if err := a.recordDeployment(input.Site, "build", "completed", "validated sandbox artifact", result, release); err != nil {
		historyUnavailable(w, r)
		return
	}
	previous, err := a.activatePipelineRelease(operationCtx, input.Site, release)
	if err != nil {
		_ = a.recordDeployment(input.Site, "activation", "failed", err.Error(), result, "") // failure already marks readiness
		http.Error(w, "atomic activation failed", 503)
		return
	}
	outcome = input.Repository + "@" + commit
	result.Previous = previous
	a.finishCommittedRelease(operationCtx, siteRoot, "activation", "atomic built release activated", release, &result, false)
	writeJSON(w, 202, result)
}

func copyGitArtifactTree(ctx context.Context, source, destination string, maxBytes, expectedBytes int64) error {
	if maxBytes < 1 || expectedBytes < 0 || expectedBytes > maxBytes {
		return errors.New("invalid release artifact copy size")
	}
	rootInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return errors.New("release artifact root is not a real directory")
	}
	destinationInfo, err := os.Lstat(destination)
	if err != nil {
		return err
	}
	if destinationInfo.Mode()&os.ModeSymlink != 0 || !destinationInfo.IsDir() {
		return errors.New("release staging root is not a real directory")
	}
	var total int64
	err = filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("release artifact contains a symlink: %s", filepath.Base(path))
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(destination, rel)
		if err := rejectSymlinkParents(target, destination); err != nil {
			return err
		}
		if existing, statErr := os.Lstat(target); statErr == nil {
			if existing.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("release staging destination is a symlink: %s", filepath.Base(target))
			}
			return fmt.Errorf("release staging destination already exists: %s", filepath.Base(target))
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if info.IsDir() {
			mode := (info.Mode().Perm() & 0555) | 0700
			if err := os.Mkdir(target, mode); err != nil {
				return err
			}
			return os.Chmod(target, mode)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("release artifact contains a special file: %s", filepath.Base(path))
		}
		if info.Size() < 0 || info.Size() > maxBytes-total {
			return errors.New("release artifact changed beyond its admitted size")
		}
		in, _, err := openRegularNoFollow(path, info)
		if err != nil {
			return err
		}
		mode := (info.Mode().Perm() & 0555) | 0600
		out, err := openWriteNoFollow(target, mode)
		if err != nil {
			_ = in.Close()
			return err
		}
		written, copyErr := io.CopyN(out, contextReader{ctx: ctx, reader: in}, info.Size())
		if copyErr == nil && written != info.Size() {
			copyErr = io.ErrUnexpectedEOF
		}
		if copyErr == nil {
			copyErr = out.Chmod(mode)
		}
		if copyErr == nil {
			copyErr = out.Sync()
		}
		closeOutErr := out.Close()
		closeInErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
		if closeInErr != nil {
			return closeInErr
		}
		total += written
		return nil
	})
	if err != nil {
		return err
	}
	if total != expectedBytes {
		return fmt.Errorf("release artifact changed during staging: copied %d bytes, expected %d", total, expectedBytes)
	}
	return nil
}

func removeSiteBuildArtifact(webRoot, site string) error {
	web, err := os.OpenRoot(webRoot)
	if err != nil {
		return err
	}
	defer web.Close()
	sites, err := openChildRoot(web, "sites")
	if err != nil {
		return err
	}
	defer sites.Close()
	siteRoot, err := openChildRoot(sites, site)
	if err != nil {
		return err
	}
	defer siteRoot.Close()
	artifact, err := siteRoot.Lstat(".stepanel-artifact")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if artifact.Mode()&os.ModeSymlink != 0 || !artifact.IsDir() {
		return errors.New("release artifact changed to a non-directory")
	}
	return siteRoot.RemoveAll(".stepanel-artifact")
}

func (a *App) checkoutPipelineRelease(ctx context.Context, site SiteCapability, repository gitRepository, ref string) (string, string, error) {
	releaseGitSlot, err := a.acquireGitCloneAdmission(ctx)
	if err != nil {
		return "", "", fmt.Errorf("Git pipeline rejected by host memory admission: %w", err)
	}
	defer releaseGitSlot()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", "", err
	}
	release, err := a.createSiteReleaseStaging(ctx, site.Site(), ".stepanel-release-")
	if err != nil {
		return "", "", err
	}
	stageLimit, err := gitCloneStageLimit(a.Config.GitReleaseMaxTreeBytes)
	if err != nil {
		logStagingDiscardFailure(release, a.discardSiteReleaseStaging(context.Background(), site.Site(), release))
		return "", "", err
	}
	reservation, err := a.capacityLedger().reserve(a.Config, "git-pipeline-checkout", release, []capacityDemand{{Path: release, Bytes: uint64(stageLimit)}})
	if err != nil {
		logStagingDiscardFailure(release, a.discardSiteReleaseStaging(context.Background(), site.Site(), release))
		return "", "", fmt.Errorf("admit aggregate Git clone staging: %w", err)
	}
	defer reservation.release()
	projectID, err := a.enableGitStageQuota(ctx, site.Site(), release, stageLimit)
	if err != nil {
		logStagingDiscardFailure(release, a.discardSiteReleaseStaging(context.Background(), site.Site(), release))
		return "", "", fmt.Errorf("Git pipeline requires enforceable project quotas: %w", err)
	}
	quotaActive := true
	defer func() {
		if quotaActive {
			if clearErr := a.disableGitStageQuota(ctx, site.Site(), release, projectID); clearErr != nil {
				log.Printf("failed to clear Git pipeline project quota for %s: %v", filepath.Base(release), clearErr)
			}
		}
	}()
	var output []byte
	if repository.Private {
		output, err = clonePrivateRepository(ctx, a.Config, site.Site(), repository.URL, ref, release, a.Config.GitReleaseMaxTreeBytes, projectID)
	} else {
		prlimit, lookupErr := exec.LookPath("prlimit")
		if lookupErr != nil {
			logStagingDiscardFailure(release, a.discardSiteReleaseStaging(context.Background(), site.Site(), release))
			return "", "", errors.New("prlimit is required to enforce Git clone memory and per-file limits")
		}
		cmd := exec.CommandContext(ctx, prlimit, "--as="+strconv.FormatUint(gitCloneAddressSpaceBytes, 10), "--fsize="+strconv.FormatInt(a.Config.GitReleaseMaxTreeBytes, 10), "--", gitPath, "-c", "credential.helper=", "clone", "--depth", "1", "--branch", ref, "--single-branch", "--no-tags", repository.URL, release)
		cmd.Env = gitCommandEnv()
		output, err = runBoundedCommand(ctx, cmd)
	}
	if err != nil {
		logStagingDiscardFailure(release, a.discardSiteReleaseStaging(context.Background(), site.Site(), release))
		return "", "", err
	}
	commit, err := identifyGitCommit(ctx, gitPath, release)
	if err != nil {
		logStagingDiscardFailure(release, a.discardSiteReleaseStaging(context.Background(), site.Site(), release))
		return "", "", err
	}
	if validateGitRelease(release, a.Config.MaxEntries) != nil || validateGitReleaseSize(release, a.Config.GitReleaseMaxTreeBytes) != nil || os.RemoveAll(filepath.Join(release, ".git")) != nil {
		logStagingDiscardFailure(release, a.discardSiteReleaseStaging(context.Background(), site.Site(), release))
		return "", "", fmt.Errorf("invalid checked-out release: %s", strings.TrimSpace(string(output)))
	}
	if err := a.disableGitStageQuota(ctx, site.Site(), release, projectID); err != nil {
		logStagingDiscardFailure(release, a.discardSiteReleaseStaging(context.Background(), site.Site(), release))
		return "", "", fmt.Errorf("clear temporary Git staging quota: %w", err)
	}
	quotaActive = false
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
	request, err := runnerRequestFromArgs(args)
	if err != nil {
		return err
	}
	return runRunnerBuild(ctx, a.Config, request)
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
	recoveryRoot := a.Config.RecoveryRoot
	if recoveryRoot == "" {
		recoveryRoot = filepath.Join(a.Config.WebRoot, "sites", ".stepanel-recovery")
	}
	journal, err := newReleaseActivationJournal(recoveryRoot, site, release)
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
	processKillInjection("deploy", "activate")
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
