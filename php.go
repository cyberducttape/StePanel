package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type PHPProfile struct {
	Site              string `json:"site"`
	Version           string `json:"version"`
	MemoryLimit       string `json:"memory_limit"`
	MaxExecutionTime  int    `json:"max_execution_time"`
	UploadMaxFilesize string `json:"upload_max_filesize"`
	PostMaxSize       string `json:"post_max_size"`
	MaxInputVars      int    `json:"max_input_vars"`
	OPcache           bool   `json:"opcache"`
	DisplayErrors     bool   `json:"display_errors"`
	ErrorReporting    string `json:"error_reporting"`
	State             string `json:"state,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}
type PHPProfileStore struct {
	mu     sync.RWMutex
	path   string
	values map[string]PHPProfile
}

var phpSizePattern = regexp.MustCompile(`^[1-9][0-9]{0,4}M$`)

func OpenPHPProfileStore(path string) (*PHPProfileStore, error) {
	s := &PHPProfileStore{path: path, values: map[string]PHPProfile{}}
	d, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(d, &s.values); e != nil {
		return nil, e
	}
	for site, profile := range s.values {
		if profile.State == "" {
			profile.State = "applied"
		}
		s.values[site] = profile
	}
	return s, nil
}
func (s *PHPProfileStore) save(site string, p PHPProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.values[site]
	s.values[site] = p
	d, e := json.MarshalIndent(s.values, "", "  ")
	if e != nil {
		return e
	}
	if bound, err := persistBoundControlPlaneState(s, d); bound {
		if err != nil {
			if existed {
				s.values[site] = previous
			} else {
				delete(s.values, site)
			}
		}
		return err
	}
	if err := writeAtomic(s.path, append(d, '\n'), 0600); err != nil {
		if existed {
			s.values[site] = previous
		} else {
			delete(s.values, site)
		}
		return err
	}
	return nil
}
func (s *PHPProfileStore) get(site string) (PHPProfile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.values[site]
	return p, ok
}
func installedPHPVersions() []string {
	entries, _ := filepath.Glob("/etc/php/*/fpm/pool.d")
	out := []string{}
	for _, entry := range entries {
		v := filepath.Base(filepath.Dir(filepath.Dir(entry)))
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func (a *App) phpRuntime(w http.ResponseWriter, r *http.Request) {
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/php/"), "/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid site", 422)
		return
	}
	if !a.canAccessSite(r, site) {
		http.Error(w, "site is not assigned to this account", 403)
		return
	}
	if r.Method == http.MethodGet {
		p, ok := a.PHP.get(site)
		writeJSON(w, 200, map[string]any{"site": site, "profile": p, "configured": ok, "versions": installedPHPVersions(), "extensions": []string{"curl", "gd", "intl", "mbstring", "mysqli", "opcache", "zip"}})
		return
	}
	if r.Method != http.MethodPut || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var p PHPProfile
	if e := decodeJSON(w, r, 4096, &p); e != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	p.Site = site
	p.Version = strings.TrimPrefix(p.Version, "v")
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+$`).MatchString(p.Version) || !phpSizePattern.MatchString(p.MemoryLimit) || !phpSizePattern.MatchString(p.UploadMaxFilesize) || !phpSizePattern.MatchString(p.PostMaxSize) || p.MaxExecutionTime < 1 || p.MaxExecutionTime > 3600 || p.MaxInputVars < 1 || p.MaxInputVars > 1000000 || !regexp.MustCompile(`^[A-Z0-9_~& |]{1,80}$`).MatchString(p.ErrorReporting) {
		http.Error(w, "invalid PHP profile", 422)
		return
	}
	releaseUnlock := a.siteOperations.Acquire(site)
	defer releaseUnlock()
	p.State, p.LastError = "pending", ""
	if e := a.PHP.save(site, p); e != nil {
		http.Error(w, "could not persist desired PHP profile", 503)
		return
	}
	if e := a.applyPHPProfile(r.Context(), p); e != nil {
		p.State, p.LastError = "pending", e.Error()
		_ = a.PHP.save(site, p)
		http.Error(w, "PHP runtime profile is pending reconciliation", 502)
		return
	}
	p.State, p.LastError = "applied", ""
	if e := a.PHP.save(site, p); e != nil {
		http.Error(w, "PHP profile applied but state update is pending", 503)
		return
	}
	_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.php.updated", site, p.Version)
	writeJSON(w, 202, p)
}

func (a *App) applyPHPProfile(ctx context.Context, p PHPProfile) error {
	return runHelperCommand(ctx, a.Config, a.Config.SiteCtl, "runtime", p.Site, p.Version, p.MemoryLimit, itoa(p.MaxExecutionTime), p.UploadMaxFilesize, p.PostMaxSize, itoa(p.MaxInputVars), boolString(p.OPcache), boolString(p.DisplayErrors), p.ErrorReporting)
}

func (a *App) reconcilePHPProfiles(ctx context.Context) (reconciled []string, failed map[string]string) {
	failed = map[string]string{}
	if a.PHP == nil {
		return nil, failed
	}
	a.PHP.mu.RLock()
	pending := make([]PHPProfile, 0)
	for _, profile := range a.PHP.values {
		if profile.State == "pending" {
			pending = append(pending, profile)
		}
	}
	a.PHP.mu.RUnlock()
	for _, profile := range pending {
		releaseUnlock := a.siteOperations.Acquire(profile.Site)
		if err := a.applyPHPProfile(ctx, profile); err != nil {
			profile.LastError = err.Error()
			_ = a.PHP.save(profile.Site, profile)
			failed[profile.Site] = err.Error()
			releaseUnlock()
			continue
		}
		profile.State, profile.LastError = "applied", ""
		if err := a.PHP.save(profile.Site, profile); err != nil {
			failed[profile.Site] = err.Error()
			releaseUnlock()
			continue
		}
		reconciled = append(reconciled, profile.Site)
		releaseUnlock()
	}
	return reconciled, failed
}
func itoa(v int) string { return fmt.Sprintf("%d", v) }
