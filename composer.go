package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
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

func OpenComposerStore(path string) (*ComposerStore, error) {
	s := &ComposerStore{path: path, latest: map[string]ComposerOperation{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
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
	s.latest[siteName] = op
	data, err := json.MarshalIndent(s.latest, "", "  ")
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(s, data); bound {
		return err
	}
	return writeAtomic(s.path, append(data, '\n'), 0600)
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
	root := filepath.Join(a.Config.WebRoot, "sites", site, "public")
	if err := ensureInside(a.Config.WebRoot, root); err != nil {
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
	var input struct {
		Development        bool `json:"development"`
		OptimizeAutoloader bool `json:"optimize_autoloader"`
	}
	if err := decodeJSON(w, r, 2048, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	releaseUnlock, lockErr := a.acquireSiteMutationLock(r.Context(), site)
	if lockErr != nil {
		http.Error(w, "Composer operation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	started := time.Now()
	if err := runHelperCommandWithTimeout(r.Context(), a.Config, helperPackageBuildTimeout, a.Config.AppCtl, "composer-install", site, root, boolString(input.Development), boolString(input.OptimizeAutoloader)); err != nil {
		http.Error(w, "Composer install failed", 502)
		return
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
		http.Error(w, "Composer succeeded but operation state could not be saved", 503)
		return
	}
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "composer.install", site, command)
	writeJSON(w, 202, op)
}
