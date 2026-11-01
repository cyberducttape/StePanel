package stepanel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/operations"
)

type nodeDeploymentRequest struct {
	Site    string `json:"site"`
	Domain  string `json:"domain"`
	Version string `json:"node_version"`
	Port    int    `json:"port"`
	Backend string `json:"backend"`
	Actor   string `json:"actor"`
	Token   bool   `json:"token,omitempty"`
	TokenID string `json:"token_id,omitempty"`
	queuedSession
	Stage    string                  `json:"stage,omitempty"`
	Snapshot *nodeDeploymentSnapshot `json:"recovery_snapshot,omitempty"`
}

type nodeDeploymentSnapshot struct {
	NVMRC          []byte `json:"nvmrc,omitempty"`
	Manifest       []byte `json:"manifest,omitempty"`
	ManifestBak    []byte `json:"manifest_backup,omitempty"`
	Proxy          []byte `json:"proxy,omitempty"`
	HadNVMRC       bool   `json:"had_nvmrc"`
	HadManifest    bool   `json:"had_manifest"`
	HadManifestBak bool   `json:"had_manifest_backup"`
	HadProxy       bool   `json:"had_proxy"`
	ProxyPath      string `json:"proxy_path"`
}

const (
	maxNodeVersionFileBytes = 4 << 10
	maxProxyConfigFileBytes = 1 << 20
)

func (a *App) startNodeDeployment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input nodeDeploymentRequest
	if err := decodeJSON(w, r, 8192, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	// These fields are worker-owned checkpoints and credential identity. Never
	// accept them from the deployment API request body.
	input.Token = false
	input.TokenID = ""
	input.queuedSession = queuedSession{}
	input.Stage = ""
	input.Snapshot = nil
	input.Site = safeUser(input.Site)
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	input.Version = strings.TrimSpace(input.Version)
	input.Backend = strings.TrimSpace(input.Backend)
	if input.Site == "" || len(input.Domain)+len(input.Site) > 220 || !domainname.Valid(input.Domain) || !nodeVersionPattern.MatchString(input.Version) || input.Port < 1024 || input.Port > 65535 {
		http.Error(w, "invalid deployment", http.StatusUnprocessableEntity)
		return
	}
	if _, err := localBackend("http://127.0.0.1:" + strconv.Itoa(input.Port)); err != nil {
		writePublicError(w, r, http.StatusUnprocessableEntity, publicError("invalid_backend", "the deployment backend is invalid", err))
		return
	}
	if _, err := localBackend(input.Backend); err != nil {
		writePublicError(w, r, http.StatusUnprocessableEntity, publicError("invalid_backend", "the deployment backend is invalid", err))
		return
	}
	if _, ok := a.requireSiteAccess(w, r, input.Site, "site is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for deployment", http.StatusForbidden)
		return
	}
	input.Actor = a.Auth.UsernameForRequest(r)
	input.Token = a.Auth.IsAPITokenRequest(r)
	input.TokenID = a.Auth.APITokenID(r)
	if input.Token && input.TokenID == "" {
		http.Error(w, "API token identity is unavailable; submit the deployment again", http.StatusUnauthorized)
		return
	}
	binding, err := a.bindQueuedSession(r, input.Actor)
	if err != nil {
		http.Error(w, "session state is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	input.queuedSession = binding
	payload, err := json.Marshal(input)
	if err != nil {
		http.Error(w, "could not encode deployment", http.StatusInternalServerError)
		return
	}
	operationKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if operationKey != "" && !validJobOperationKey(operationKey) {
		http.Error(w, "invalid Idempotency-Key", http.StatusUnprocessableEntity)
		return
	}
	job, _, err := a.Jobs.EnqueueIdempotent("node.deployment", input.Site, operationKey, payload, 3)
	if err != nil {
		writeJobEnqueueError(w, err, "could not persist deployment")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID})
}

func (a *App) handleNodeDeploymentJob(ctx context.Context, item Job) ([]byte, error) {
	var input nodeDeploymentRequest
	if err := json.Unmarshal(item.Payload, &input); err != nil {
		return nil, fmt.Errorf("decode node deployment: %w", err)
	}
	if _, err := a.authorizeDurableSiteJob(input.Site, input.Actor, false); err != nil {
		return nil, err
	}
	principal, err := a.nodeDeploymentPrincipal(input)
	if err != nil && input.Stage == "" {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	operationCtx, releaseLock, err := a.acquireSiteMutationLockContext(ctx, input.Site)
	if err != nil {
		return nil, fmt.Errorf("deployment is busy: %w", err)
	}
	defer releaseLock()
	if input.Stage == "completed" {
		return nodeDeploymentResult(input), nil
	}
	if input.Stage != "" {
		if err := a.recoverNodeDeploymentCheckpoint(operationCtx, item, &input); err != nil {
			return nil, err
		}
		// A revoked token may still need its already-started partial deployment
		// compensated, but must not authorize starting it again.
		principal, err = a.nodeDeploymentPrincipal(input)
		if err != nil {
			return nil, err
		}
	}
	snapshot, err := a.snapshotNodeDeployment(input)
	if err != nil {
		return nil, fmt.Errorf("snapshot deployment state: %w", err)
	}
	input.Snapshot = &snapshot
	input.Stage = "prepared"
	if err := a.persistNodeDeploymentCheckpoint(item, input); err != nil {
		return nil, fmt.Errorf("persist Node deployment recovery snapshot: %w", err)
	}
	for _, deploymentStep := range []struct {
		name string
		run  func() error
	}{
		{name: "select-node", run: func() error { return a.applyNodeSelection(operationCtx, principal, input.Site, input.Version) }},
		{name: "deploy-app", run: func() error {
			_, _, _, err := a.applyAppDeployment(operationCtx, principal, AppManifest{Site: input.Site, Domain: input.Domain, Version: input.Version, Port: input.Port})
			return err
		}},
		{name: "deploy-proxy", run: func() error {
			_, err := a.applyProxyDeployment(operationCtx, principal, proxyRequest{Site: input.Site, Domain: input.Domain, Backend: input.Backend})
			return err
		}},
	} {
		input.Stage = deploymentStep.name + "-applying"
		if err := a.persistNodeDeploymentCheckpoint(item, input); err != nil {
			return nil, fmt.Errorf("persist Node deployment stage %s: %w", input.Stage, err)
		}
		if err := deploymentStep.run(); err != nil {
			cause := fmt.Errorf("%s: %w", deploymentStep.name, err)
			rollbackErr := a.rollbackNodeDeployment(operationCtx, item, input, snapshot)
			if rollbackErr == nil {
				input.Stage = ""
				input.Snapshot = nil
				if clearErr := a.persistNodeDeploymentCheckpoint(item, input); clearErr != nil {
					rollbackErr = fmt.Errorf("previous deployment restored but checkpoint clear failed: %w", clearErr)
				}
			}
			if rollbackErr != nil {
				return nil, fmt.Errorf("%w; rollback deferred or incomplete: %v", cause, rollbackErr)
			}
			return nil, fmt.Errorf("%w; previous deployment restored", cause)
		}
		input.Stage = deploymentStep.name + "-applied"
		if err := a.persistNodeDeploymentCheckpoint(item, input); err != nil {
			return nil, fmt.Errorf("persist Node deployment stage %s: %w", input.Stage, err)
		}
	}
	input.Stage = "completed"
	if err := a.persistNodeDeploymentCheckpoint(item, input); err != nil {
		return nil, fmt.Errorf("persist completed Node deployment state: %w", err)
	}
	return nodeDeploymentResult(input), nil
}

func (a *App) recoverNodeDeploymentCheckpoint(ctx context.Context, item Job, input *nodeDeploymentRequest) error {
	if input == nil || input.Stage == "" {
		return nil
	}
	if input.Stage == "completed" {
		return nil
	}
	if input.Snapshot == nil {
		return errors.New("interrupted Node deployment has no recovery snapshot; operator reconciliation is required")
	}
	stage := input.Stage
	if err := a.rollbackNodeDeployment(ctx, item, *input, *input.Snapshot); err != nil {
		return fmt.Errorf("recover interrupted Node deployment at %s: %w", stage, err)
	}
	input.Stage = ""
	input.Snapshot = nil
	if err := a.persistNodeDeploymentCheckpoint(item, *input); err != nil {
		return fmt.Errorf("clear recovered Node deployment checkpoint: %w", err)
	}
	return nil
}

func (a *App) nodeDeploymentPrincipal(input nodeDeploymentRequest) (siteOperationPrincipal, error) {
	principal := siteOperationPrincipal{Actor: input.Actor, Token: input.Token}
	if input.Token {
		token, err := a.revalidateQueuedToken(siteOperationPayload{Actor: input.Actor, Token: true, TokenID: input.TokenID})
		if err != nil {
			return siteOperationPrincipal{}, err
		}
		principal.Actor = token.Username
		principal.Scopes = append([]string(nil), token.Scopes...)
	} else if err := a.revalidateQueuedSession(input.Actor, input.queuedSession); err != nil {
		return siteOperationPrincipal{}, err
	}
	if !a.durableSiteOperationScopeAllowed(principal, "site:deploy") {
		return siteOperationPrincipal{}, errors.New("Node deployment authorization: site:deploy permission is no longer active")
	}
	return principal, nil
}

func (a *App) persistNodeDeploymentCheckpoint(item Job, input nodeDeploymentRequest) error {
	if a.Jobs == nil {
		return errors.New("Node deployment requires the durable job store")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return a.Jobs.UpdateClaimPayload(item.ID, item.LeaseToken, data)
}

func nodeDeploymentResult(input nodeDeploymentRequest) []byte {
	result, _ := json.Marshal(map[string]any{"site": input.Site, "domain": input.Domain, "node_version": input.Version, "port": input.Port, "backend": input.Backend, "committed": true})
	return result
}

func (a *App) snapshotNodeDeployment(input nodeDeploymentRequest) (nodeDeploymentSnapshot, error) {
	root, err := existingManagedSiteState(a.Config, input.Site)
	if err != nil {
		return nodeDeploymentSnapshot{}, err
	}
	snapshot := nodeDeploymentSnapshot{}
	snapshot.NVMRC, snapshot.HadNVMRC, err = readOptionalFile(filepath.Join(root, ".nvmrc"), maxNodeVersionFileBytes)
	if err != nil {
		return snapshot, err
	}
	snapshot.Manifest, snapshot.HadManifest, err = readOptionalFile(filepath.Join(a.Config.AppRoot, input.Site+".json"), maxAppStateFileBytes)
	if err != nil {
		return snapshot, err
	}
	if snapshot.HadManifest {
		var previous AppManifest
		if err := json.Unmarshal(snapshot.Manifest, &previous); err != nil || !validAppManifest(a.Config, previous, input.Site) {
			return snapshot, errors.New("existing app manifest is invalid; refusing to replace unmanaged runtime state")
		}
	}
	snapshot.ManifestBak, snapshot.HadManifestBak, err = readOptionalFile(filepath.Join(a.Config.AppRoot, input.Site+".json.bak"), maxAppStateFileBytes)
	if err != nil {
		return snapshot, err
	}
	snapshot.ProxyPath, err = safePath(a.Config.ProxyRoot, proxyConfigName(a.Config.WebServer, input.Site, input.Domain))
	if err != nil {
		return snapshot, err
	}
	snapshot.Proxy, snapshot.HadProxy, err = readOptionalFile(snapshot.ProxyPath, maxProxyConfigFileBytes)
	return snapshot, err
}

func readOptionalFile(path string, maxBytes int64) ([]byte, bool, error) {
	if maxBytes <= 0 {
		return nil, false, errors.New("optional file size limit must be positive")
	}
	file, _, err := openRegularNoFollow(path, nil)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, false, readErr
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	if int64(len(data)) > maxBytes {
		return nil, false, fmt.Errorf("%w: optional file exceeds %d bytes", errStateFileTooLarge, maxBytes)
	}
	return data, true, nil
}

func restoreOptionalFile(path string, data []byte, existed bool, mode os.FileMode) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAtomic(path, data, mode)
}

var (
	apacheProxyBackendPattern = regexp.MustCompile(`(?m)^\s*ProxyPass / http://([^/]+)/`)
	caddyProxyBackendPattern  = regexp.MustCompile(`(?m)^\s*reverse_proxy\s+([^\s{}]+)\s*$`)
)

func (a *App) rollbackNodeDeployment(ctx context.Context, item Job, input nodeDeploymentRequest, snapshot nodeDeploymentSnapshot) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), helperConfigMutationTimeout)
	defer cancel()
	if err := a.verifyNodeDeploymentOwnership(cleanupCtx, ctx, item); err != nil {
		return fmt.Errorf("refusing Node deployment rollback without current lease ownership: %w", err)
	}
	root, err := existingManagedSiteState(a.Config, input.Site)
	if err == nil {
		if err = a.verifyNodeDeploymentOwnership(cleanupCtx, ctx, item); err == nil {
			err = restoreOptionalFile(filepath.Join(root, ".nvmrc"), snapshot.NVMRC, snapshot.HadNVMRC, 0640)
		}
	}
	if err == nil {
		if err = a.verifyNodeDeploymentOwnership(cleanupCtx, ctx, item); err == nil {
			err = restoreOptionalFile(filepath.Join(a.Config.AppRoot, input.Site+".json"), snapshot.Manifest, snapshot.HadManifest, 0600)
		}
	}
	if err == nil {
		if err = a.verifyNodeDeploymentOwnership(cleanupCtx, ctx, item); err == nil {
			err = restoreOptionalFile(filepath.Join(a.Config.AppRoot, input.Site+".json.bak"), snapshot.ManifestBak, snapshot.HadManifestBak, 0600)
		}
	}
	if err == nil {
		if err = a.verifyNodeDeploymentOwnership(cleanupCtx, ctx, item); err == nil {
			if snapshot.HadManifest {
				var previous AppManifest
				if unmarshalErr := json.Unmarshal(snapshot.Manifest, &previous); unmarshalErr != nil || !validAppManifest(a.Config, previous, input.Site) {
					err = errors.New("previous app manifest is invalid; runtime rollback refused")
				} else {
					err = restoreAppProcess(cleanupCtx, a.Config, previous)
				}
			} else {
				err = runAppLifecycle(cleanupCtx, a.Config, "stop", input.Site)
			}
		}
	}
	if err == nil {
		if snapshot.HadProxy {
			backend, backendErr := previousNodeProxyBackend(a.Config.WebServer, snapshot.Proxy)
			if backendErr != nil {
				err = backendErr
			} else {
				if err = a.verifyNodeDeploymentOwnership(cleanupCtx, ctx, item); err == nil {
					err = runProxyMutation(cleanupCtx, a.Config, "apply", input.Site, input.Domain, backend)
				}
			}
		} else {
			if err = a.verifyNodeDeploymentOwnership(cleanupCtx, ctx, item); err == nil {
				err = deleteProxyRoute(cleanupCtx, a.Config, filepath.Base(snapshot.ProxyPath), input.Site, input.Domain)
			}
			if err != nil && strings.Contains(err.Error(), "proxy not found") {
				err = nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}
	return nil
}

func previousNodeProxyBackend(webServer string, config []byte) (string, error) {
	var match [][]byte
	switch strings.ToLower(strings.TrimSpace(webServer)) {
	case "caddy":
		match = caddyProxyBackendPattern.FindSubmatch(config)
	case "apache":
		match = apacheProxyBackendPattern.FindSubmatch(config)
	default:
		return "", errors.New("previous proxy configuration cannot be safely restored for this web server")
	}
	if len(match) != 2 {
		return "", errors.New("previous proxy configuration has no recoverable backend")
	}
	backend, err := localBackend("http://" + string(match[1]))
	if err != nil {
		return "", fmt.Errorf("previous proxy backend is invalid: %w", err)
	}
	return backend, nil
}

func (a *App) verifyNodeDeploymentOwnership(ctx, leaseContext context.Context, item Job) error {
	if a.Jobs == nil || a.Jobs.db == nil || item.ID == "" || item.LeaseToken == "" {
		return errJobLeaseNotHeld
	}
	var present int
	err := a.Jobs.db.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE id=? AND state='running' AND lease_token=? AND lease_expires_at>?`, item.ID, item.LeaseToken, time.Now().UTC().UnixNano()).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return errJobLeaseNotHeld
	}
	if err != nil {
		return fmt.Errorf("verify durable job lease: %w", err)
	}
	for _, token := range operations.FencingTokens(leaseContext) {
		if err := operations.VerifyFencingTokenWithinContext(ctx, a.Jobs.db, token, 0); err != nil {
			return err
		}
	}
	return nil
}
