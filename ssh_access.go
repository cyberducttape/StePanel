package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

type SSHKey struct {
	Label       string `json:"label"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
}
type SiteAccess struct {
	Site         string   `json:"site"`
	SFTPEnabled  bool     `json:"sftp_enabled"`
	ShellEnabled bool     `json:"shell_enabled"`
	Keys         []SSHKey `json:"keys"`
	State        string   `json:"state,omitempty"`
	LastError    string   `json:"last_error,omitempty"`
}
type SiteAccessStore struct {
	mu     sync.RWMutex
	path   string
	values map[string]SiteAccess
}

func OpenSiteAccessStore(path string) (*SiteAccessStore, error) {
	s := &SiteAccessStore{path: path, values: map[string]SiteAccess{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &s.values); err != nil {
		return nil, fmt.Errorf("decode SSH access state: %w", err)
	}
	for site, access := range s.values {
		if access.Site == "" {
			access.Site = site
		}
		if access.State == "" {
			access.State = "applied"
		}
		s.values[site] = access
	}
	return s, nil
}
func (s *SiteAccessStore) persistLocked() error {
	data, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(s, data); bound {
		return err
	}
	return writeAtomic(s.path, append(data, '\n'), 0600)
}
func validateSSHLabel(label string) bool {
	if label == "" || len(label) > 64 {
		return false
	}
	for _, c := range label {
		if !(c == '-' || c == '_' || c == '.' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func parseSSHKey(raw string) (SSHKey, error) {
	key, _, options, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(raw)))
	if err != nil {
		return SSHKey{}, errors.New("invalid SSH public key")
	}
	if len(options) != 0 {
		return SSHKey{}, errors.New("SSH key options are managed by StePanel and are not permitted")
	}
	typ := key.Type()
	if typ == "ssh-dss" {
		return SSHKey{}, errors.New("DSA SSH keys are not permitted")
	}
	return SSHKey{PublicKey: strings.TrimSpace(raw), Fingerprint: ssh.FingerprintSHA256(key)}, nil
}

func (a *App) applySiteAccess(ctx context.Context, access SiteAccess) error {
	if a.Config.SiteCtl == "" {
		return errors.New("site access helper is not configured")
	}
	keys := make([]string, 0, len(access.Keys))
	if access.SFTPEnabled || access.ShellEnabled {
		for _, key := range access.Keys {
			keys = append(keys, key.PublicKey)
		}
	}
	payload := strings.Join(keys, "\n")
	if payload != "" {
		payload += "\n"
	}
	commandCtx, cancel := context.WithTimeout(ctx, helperConfigMutationTimeout)
	defer cancel()
	_, err := runBoundedCommandInput(commandCtx, helperCommandContext(commandCtx, a.Config, a.Config.SiteCtl, "access", access.Site, stringBool(access.SFTPEnabled), stringBool(access.ShellEnabled)), strings.NewReader(payload))
	return err
}

func (a *App) saveSiteAccess(access SiteAccess) error {
	a.Access.mu.Lock()
	defer a.Access.mu.Unlock()
	a.Access.values[access.Site] = access
	return a.Access.persistLocked()
}

func (a *App) applyAndSaveSiteAccess(ctx context.Context, access SiteAccess) (SiteAccess, error) {
	if err := a.applySiteAccess(ctx, access); err != nil {
		access.State = "pending"
		access.LastError = err.Error()
		if saveErr := a.saveSiteAccess(access); saveErr != nil {
			return access, fmt.Errorf("apply site access: %w; save pending state: %v", err, saveErr)
		}
		return access, err
	}
	access.State = "applied"
	access.LastError = ""
	return access, a.saveSiteAccess(access)
}

func (a *App) reconcileSiteAccess(ctx context.Context) (reconciled []string, failed map[string]string) {
	failed = map[string]string{}
	if a.Access == nil {
		return nil, failed
	}
	a.Access.mu.RLock()
	pending := make([]SiteAccess, 0)
	for _, access := range a.Access.values {
		if access.State == "pending" {
			pending = append(pending, access)
		}
	}
	a.Access.mu.RUnlock()
	for _, access := range pending {
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(ctx, access.Site)
		if lockErr != nil {
			failed[access.Site] = lockErr.Error()
			continue
		}
		if _, err := a.applyAndSaveSiteAccess(operationCtx, access); err != nil {
			failed[access.Site] = err.Error()
			releaseUnlock()
			continue
		}
		reconciled = append(reconciled, access.Site)
		releaseUnlock()
	}
	return reconciled, failed
}

func (a *App) siteAccess(w http.ResponseWriter, r *http.Request) {
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/access/"), "/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid site", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", 403); !ok {
		return
	}
	if a.Access == nil {
		http.Error(w, "SSH access state unavailable", 503)
		return
	}
	a.Access.mu.RLock()
	access := a.Access.values[site]
	if access.Site == "" {
		access = SiteAccess{Site: site, SFTPEnabled: true, Keys: []SSHKey{}, State: "unconfigured"}
	}
	a.Access.mu.RUnlock()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, access)
	case http.MethodPatch:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		if !a.Auth.HasRequiredCustomerScope(r, "ssh:write") {
			http.Error(w, "API token lacks the ssh:write scope", 403)
			return
		}
		var input struct {
			SFTPEnabled  *bool `json:"sftp_enabled"`
			ShellEnabled *bool `json:"shell_enabled"`
		}
		if err := decodeJSON(w, r, 2048, &input); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
		if lockErr != nil {
			http.Error(w, "SSH access mutation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		if input.SFTPEnabled != nil {
			access.SFTPEnabled = *input.SFTPEnabled
		}
		if input.ShellEnabled != nil {
			access.ShellEnabled = *input.ShellEnabled
		}
		access.Site = site
		access.State, access.LastError = "pending", ""
		a.Access.mu.Lock()
		a.Access.values[site] = access
		err := a.Access.persistLocked()
		a.Access.mu.Unlock()
		if err != nil {
			http.Error(w, "SSH access state could not be saved", 503)
			return
		}
		access, err = a.applyAndSaveSiteAccess(operationCtx, access)
		if err != nil {
			http.Error(w, "SSH access is pending reconciliation", 502)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.ssh-access.updated", site, "access policy changed")
		writeJSON(w, 200, access)
	case http.MethodPost:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		if !a.Auth.HasRequiredCustomerScope(r, "ssh:write") {
			http.Error(w, "API token lacks the ssh:write scope", 403)
			return
		}
		var input struct {
			Label     string `json:"label"`
			PublicKey string `json:"public_key"`
		}
		if err := decodeJSON(w, r, 8<<10, &input); err != nil || !validateSSHLabel(input.Label) {
			http.Error(w, "invalid key label", 422)
			return
		}
		key, err := parseSSHKey(input.PublicKey)
		if err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
		key.Label = input.Label
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
		if lockErr != nil {
			http.Error(w, "SSH key mutation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		for _, existing := range access.Keys {
			if existing.Label == key.Label || existing.Fingerprint == key.Fingerprint {
				http.Error(w, "SSH key label or fingerprint already exists", 409)
				return
			}
		}
		access.Site = site
		access.State, access.LastError = "pending", ""
		access.Keys = append(access.Keys, key)
		a.Access.mu.Lock()
		a.Access.values[site] = access
		err = a.Access.persistLocked()
		a.Access.mu.Unlock()
		if err != nil {
			http.Error(w, "SSH key could not be saved", 503)
			return
		}
		access, err = a.applyAndSaveSiteAccess(operationCtx, access)
		if err != nil {
			http.Error(w, "SSH key is pending reconciliation", 502)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.ssh-key.added", site, key.Fingerprint)
		writeJSON(w, 201, key)
	}
}

func (a *App) siteAccessKey(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/access/"), "/"), "/")
	if len(parts) != 2 {
		http.Error(w, "invalid site or key label", 422)
		return
	}
	site, label := parts[0], parts[1]
	if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", 403); !ok {
		return
	}
	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "ssh:write") {
		http.Error(w, "API token lacks the ssh:write scope", 403)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
	if lockErr != nil {
		http.Error(w, "SSH key mutation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	a.Access.mu.Lock()
	access, ok := a.Access.values[site]
	if !ok {
		a.Access.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	found := false
	keys := access.Keys[:0]
	for _, key := range access.Keys {
		if key.Label == label {
			found = true
		} else {
			keys = append(keys, key)
		}
	}
	access.Keys = keys
	access.Site = site
	access.State, access.LastError = "pending", ""
	if !found {
		a.Access.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	a.Access.values[site] = access
	err := a.Access.persistLocked()
	a.Access.mu.Unlock()
	if err != nil {
		http.Error(w, "SSH key could not be removed", 503)
		return
	}
	access, err = a.applyAndSaveSiteAccess(operationCtx, access)
	if err != nil {
		http.Error(w, "SSH key revocation is pending reconciliation", 502)
		return
	}
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.ssh-key.removed", site, label)
	w.WriteHeader(204)
}

func siteAccessStatePath(cfg Config) string {
	return filepath.Join(filepath.Dir(cfg.JobState), "site-access.json")
}
