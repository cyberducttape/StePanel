package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cyberducttape/StePanel/internal/domainname"
)

type nodeDeploymentRequest struct {
	Site    string `json:"site"`
	Domain  string `json:"domain"`
	Version string `json:"node_version"`
	Port    int    `json:"port"`
	Backend string `json:"backend"`
	Actor   string `json:"actor"`
}

type nodeDeploymentContextKey struct{}

type nodeDeploymentSnapshot struct {
	nvmrc          []byte
	manifest       []byte
	manifestBak    []byte
	proxy          []byte
	hadNVMRC       bool
	hadManifest    bool
	hadManifestBak bool
	hadProxy       bool
	proxyPath      string
}

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
	input.Site = safeUser(input.Site)
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	input.Version = strings.TrimSpace(input.Version)
	input.Backend = strings.TrimSpace(input.Backend)
	if input.Site == "" || len(input.Domain)+len(input.Site) > 220 || !domainname.Valid(input.Domain) || !nodeVersionPattern.MatchString(input.Version) || input.Port < 1024 || input.Port > 65535 {
		http.Error(w, "invalid deployment", http.StatusUnprocessableEntity)
		return
	}
	if _, err := localBackend("http://127.0.0.1:" + strconv.Itoa(input.Port)); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if _, err := localBackend(input.Backend); err != nil {
		http.Error(w, "invalid backend: "+err.Error(), http.StatusUnprocessableEntity)
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
		http.Error(w, "could not persist deployment", http.StatusInternalServerError)
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	operationCtx, releaseLock, err := a.acquireSiteMutationLockContext(ctx, input.Site)
	if err != nil {
		return nil, fmt.Errorf("deployment is busy: %w", err)
	}
	defer releaseLock()
	snapshot, err := a.snapshotNodeDeployment(input)
	if err != nil {
		return nil, fmt.Errorf("snapshot deployment state: %w", err)
	}
	requestContext := context.WithValue(operationCtx, nodeDeploymentContextKey{}, true)
	requestContext = context.WithValue(requestContext, apiTokenUsernameKey{}, input.Actor)
	requestContext = context.WithValue(requestContext, apiTokenScopesKey{}, []string{"site:deploy"})
	step := func(path string, body any, handler http.HandlerFunc) error {
		data, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return marshalErr
		}
		req := httptest.NewRequestWithContext(requestContext, http.MethodPost, path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler(response, req)
		if response.Code < 200 || response.Code >= 300 {
			return errors.New(strings.TrimSpace(response.Body.String()))
		}
		return nil
	}
	if err := step("/api/node/select", map[string]string{"site": input.Site, "version": input.Version}, a.selectNode); err != nil {
		return nil, a.rollbackNodeDeployment(ctx, input, snapshot, fmt.Errorf("select Node runtime: %w", err))
	}
	if err := step("/api/apps/deploy", AppManifest{Site: input.Site, Domain: input.Domain, Version: input.Version, Port: input.Port}, a.appDeploy); err != nil {
		return nil, a.rollbackNodeDeployment(ctx, input, snapshot, fmt.Errorf("deploy application: %w", err))
	}
	if err := step("/api/proxy/deploy", proxyRequest{Site: input.Site, Domain: input.Domain, Backend: input.Backend}, a.deployProxy); err != nil {
		return nil, a.rollbackNodeDeployment(ctx, input, snapshot, fmt.Errorf("deploy proxy: %w", err))
	}
	result, _ := json.Marshal(map[string]any{"site": input.Site, "domain": input.Domain, "node_version": input.Version, "port": input.Port, "backend": input.Backend, "committed": true})
	return result, nil
}

func (a *App) snapshotNodeDeployment(input nodeDeploymentRequest) (nodeDeploymentSnapshot, error) {
	root, err := existingManagedSiteRoot(a.Config.WebRoot, input.Site)
	if err != nil {
		return nodeDeploymentSnapshot{}, err
	}
	snapshot := nodeDeploymentSnapshot{}
	snapshot.nvmrc, snapshot.hadNVMRC, err = readOptionalFile(filepath.Join(root, ".nvmrc"))
	if err != nil {
		return snapshot, err
	}
	snapshot.manifest, snapshot.hadManifest, err = readOptionalFile(filepath.Join(a.Config.AppRoot, input.Site+".json"))
	if err != nil {
		return snapshot, err
	}
	snapshot.manifestBak, snapshot.hadManifestBak, err = readOptionalFile(filepath.Join(a.Config.AppRoot, input.Site+".json.bak"))
	if err != nil {
		return snapshot, err
	}
	snapshot.proxyPath, err = safePath(a.Config.ProxyRoot, proxyConfigName(a.Config.WebServer, input.Site, input.Domain))
	if err != nil {
		return snapshot, err
	}
	snapshot.proxy, snapshot.hadProxy, err = readOptionalFile(snapshot.proxyPath)
	return snapshot, err
}

func readOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
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

var proxyBackendPattern = regexp.MustCompile(`(?m)^\s*ProxyPass / http://([^/]+)/`)

func (a *App) rollbackNodeDeployment(ctx context.Context, input nodeDeploymentRequest, snapshot nodeDeploymentSnapshot, cause error) error {
	root, err := existingManagedSiteRoot(a.Config.WebRoot, input.Site)
	if err == nil {
		err = restoreOptionalFile(filepath.Join(root, ".nvmrc"), snapshot.nvmrc, snapshot.hadNVMRC, 0640)
	}
	if err == nil {
		err = restoreOptionalFile(filepath.Join(a.Config.AppRoot, input.Site+".json"), snapshot.manifest, snapshot.hadManifest, 0600)
	}
	if err == nil {
		err = restoreOptionalFile(filepath.Join(a.Config.AppRoot, input.Site+".json.bak"), snapshot.manifestBak, snapshot.hadManifestBak, 0600)
	}
	if err == nil && snapshot.hadManifest {
		var previous AppManifest
		if json.Unmarshal(snapshot.manifest, &previous) == nil {
			err = applyAppProcess(ctx, a.Config, previous)
		}
	}
	if err == nil {
		if snapshot.hadProxy {
			match := proxyBackendPattern.FindSubmatch(snapshot.proxy)
			if len(match) != 2 {
				err = errors.New("previous proxy configuration has no recoverable backend")
			} else {
				err = runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.ProxyCtl, "apply", input.Site, input.Domain, string(match[1]))
			}
		} else {
			err = runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.ProxyCtl, "delete", strings.TrimSuffix(filepath.Base(snapshot.proxyPath), filepath.Ext(snapshot.proxyPath)))
			if err != nil && strings.Contains(err.Error(), "proxy not found") {
				err = nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("%w; rollback failed: %v", cause, err)
	}
	return fmt.Errorf("%w; previous deployment restored", cause)
}
