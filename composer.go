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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

type ComposerOperation struct {
	Site       string    `json:"site"`
	Command    string    `json:"command"`
	Completed  time.Time `json:"completed"`
	DurationMS int64     `json:"duration_ms"`
	Commit     string    `json:"commit,omitempty"`
}
type ComposerStore struct {
	mu     sync.RWMutex
	path   string
	latest map[string]ComposerOperation
}

type composerInstallRequest struct {
	Development        bool `json:"development"`
	OptimizeAutoloader bool `json:"optimize_autoloader"`
}

func decodeComposerInstall(body []byte) (composerInstallRequest, error) {
	if len(body) > 2048 {
		return composerInstallRequest{}, errors.New("Composer request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input composerInstallRequest
	if err := decoder.Decode(&input); err != nil {
		return composerInstallRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return composerInstallRequest{}, errors.New("request body must contain one JSON value")
	}
	return input, nil
}

func validateComposerInstallPayload(body []byte) error {
	_, err := decodeComposerInstall(body)
	return err
}

type composerOperationFailure struct {
	status  int
	message string
}

func (e *composerOperationFailure) Error() string { return e.message }

func composerFailureStatus(err error) int {
	var failure *composerOperationFailure
	if errors.As(err, &failure) {
		return failure.status
	}
	return http.StatusBadGateway
}

func OpenComposerStore(path string) (*ComposerStore, error) {
	s := &ComposerStore{path: path, latest: map[string]ComposerOperation{}}
	data, err := readStateFile(path, 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if errors.Is(err, errStateFileTooLarge) {
		return nil, errors.New("Composer state exceeds 4 MiB")
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &s.latest); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *ComposerStore) save(site SiteCapability, op ComposerOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	siteName := site.Site()
	return persistMapKeyChange(s.latest, siteName, &op, func() error {
		data, err := json.MarshalIndent(s.latest, "", "  ")
		if err != nil {
			return err
		}
		if bound, err := persistBoundControlPlaneState(s, data); bound {
			return err
		}
		return writeAtomic(s.path, append(data, '\n'), 0600)
	})
}
func (s *ComposerStore) get(site SiteCapability) (ComposerOperation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.latest[site.Site()]
	return v, ok
}
func composerVersion() string {
	if !commandAvailable("composer") {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := runBoundedCommand(ctx, exec.CommandContext(ctx, "composer", "--no-ansi", "--version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
func gitHead(root string) string {
	if !commandAvailable("git") {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := runBoundedCommand(ctx, exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
func boolString(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
func (a *App) composer(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/composer/"), "/"), "/")
	if len(parts) < 1 {
		http.Error(w, "invalid site", 422)
		return
	}
	site := safeUser(parts[0])
	if site == "" {
		http.Error(w, "invalid site", 422)
		return
	}
	access, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", 403)
	if !ok {
		return
	}
	root, err := siteDocRoot(a.Config, site)
	if err != nil {
		http.Error(w, "invalid site root", 422)
		return
	}
	if r.Method == http.MethodGet && len(parts) == 1 {
		_, jsonErr := os.Stat(filepath.Join(root, "composer.json"))
		_, lockErr := os.Stat(filepath.Join(root, "composer.lock"))
		op, ok := a.Composer.get(access)
		writeJSON(w, 200, map[string]any{"site": site, "composer_version": composerVersion(), "composer_json": jsonErr == nil, "composer_lock": lockErr == nil, "last_operation": op, "has_last_operation": ok})
		return
	}
	if r.Method != http.MethodPost || len(parts) != 2 || parts[1] != "install" || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for Composer operations", http.StatusForbidden)
		return
	}
	if _, err := os.Stat(filepath.Join(root, "composer.json")); err != nil {
		http.Error(w, "composer.json is required", 422)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2048))
	if err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input, err := decodeComposerInstall(body)
	if err != nil {
		http.Error(w, "invalid Composer request", http.StatusUnprocessableEntity)
		return
	}
	op, err := a.applyComposerInstall(r.Context(), a.Auth.AuditActor(r), access, input)
	if err != nil {
		http.Error(w, err.Error(), composerFailureStatus(err))
		return
	}
	writeJSON(w, 202, op)
}

// executeComposerInstallCommand is the durable worker adapter. It repeats
// request validation and current tenant/scope authorization, then invokes the
// same operation service as the HTTP adapter without replaying the route.
func executeComposerInstallCommand(a *App, ctx context.Context, command siteOperationCommand, principal siteOperationPrincipal) ([]byte, error) {
	input, err := decodeComposerInstall(command.Body)
	if err != nil || command.Site == "" {
		return nil, errors.New("invalid Composer install command")
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(command.Path, "/api/composer/"), "/"), "/")
	if len(parts) != 2 || safeUser(parts[0]) != command.Site || parts[1] != "install" {
		return nil, errors.New("invalid Composer install target")
	}
	access, err := a.authorizeDurableSiteJob(command.Site, principal.Actor, false)
	if err != nil {
		return nil, fmt.Errorf("Composer authorization: %w", err)
	}
	if !a.durableSiteOperationScopeAllowed(principal, "site:deploy") {
		return nil, errors.New("Composer authorization: required site:deploy permission is no longer active")
	}
	op, err := a.applyComposerInstall(ctx, principal.Actor, access, input)
	if err != nil {
		return nil, err
	}
	return json.Marshal(op)
}

func (a *App) applyComposerInstall(ctx context.Context, actor string, access SiteCapability, input composerInstallRequest) (ComposerOperation, error) {
	if err := ctx.Err(); err != nil {
		return ComposerOperation{}, &composerOperationFailure{http.StatusConflict, "Composer operation was cancelled before it started"}
	}
	site := access.Site()
	root, err := siteDocRoot(a.Config, site)
	if err != nil {
		return ComposerOperation{}, &composerOperationFailure{http.StatusUnprocessableEntity, "invalid site root"}
	}
	if info, err := os.Stat(filepath.Join(root, "composer.json")); err != nil || info.IsDir() {
		return ComposerOperation{}, &composerOperationFailure{http.StatusUnprocessableEntity, "composer.json is required"}
	}
	operationCtx, release, err := a.acquireSiteMutationLockContext(ctx, site)
	if err != nil {
		return ComposerOperation{}, &composerOperationFailure{http.StatusConflict, "Composer operation is busy"}
	}
	defer release()
	if err := operationCtx.Err(); err != nil {
		return ComposerOperation{}, &composerOperationFailure{http.StatusConflict, "Composer operation cancelled because the mutation lock was lost"}
	}
	started := time.Now()
	if a.Config.Production {
		err = runTypedAppOperation(operationCtx, a.Config, rootbroker.AppRequest{Action: "composer-install", Site: site, Root: root, Development: input.Development, OptimizeAutoloader: input.OptimizeAutoloader})
	} else {
		err = runHelperCommandWithTimeout(operationCtx, a.Config, helperPackageBuildTimeout, a.Config.AppCtl, "composer-install", site, root, boolString(input.Development), boolString(input.OptimizeAutoloader))
	}
	if err != nil {
		return ComposerOperation{}, &composerOperationFailure{http.StatusBadGateway, "Composer install failed"}
	}
	if err := operationCtx.Err(); err != nil {
		return ComposerOperation{}, &composerOperationFailure{http.StatusConflict, "Composer operation cancelled because the mutation lock was lost"}
	}
	command := "composer install --no-interaction --no-progress --no-scripts --no-plugins"
	if !input.Development {
		command += " --no-dev"
	}
	if input.OptimizeAutoloader {
		command += " --optimize-autoloader"
	}
	op := ComposerOperation{Site: site, Command: command, Completed: time.Now().UTC(), DurationMS: time.Since(started).Milliseconds(), Commit: gitHead(root)}
	if err := a.Composer.save(access, op); err != nil {
		return ComposerOperation{}, &composerOperationFailure{http.StatusServiceUnavailable, "Composer succeeded but operation state could not be saved"}
	}
	TelemetryAudit(a.Config.AuditLog, actor, "composer.install", site, command)
	return op, nil
}
