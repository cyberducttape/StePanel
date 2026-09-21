package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type Worker struct {
	Site      string `json:"site"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Processes int    `json:"processes"`
	MemoryMB  int    `json:"memory_mb"`
	Retries   int    `json:"retries"`
	Root      string `json:"root"`
	State     string `json:"state,omitempty"`
	LastError string `json:"last_error,omitempty"`
	Deleted   bool   `json:"deleted,omitempty"`
}
type WorkerStore struct {
	mu     sync.RWMutex
	path   string
	values map[string]Worker
}

var workerTypes = map[string]bool{"laravel": true, "horizon": true, "node": true, "celery": true, "rq": true}

func OpenWorkerStore(path string) (*WorkerStore, error) {
	s := &WorkerStore{path: path, values: map[string]Worker{}}
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
	for key, worker := range s.values {
		if worker.State == "" {
			worker.State = "applied"
		}
		s.values[key] = worker
	}
	return s, nil
}
func (s *WorkerStore) persistLocked() error {
	d, e := json.MarshalIndent(s.values, "", "  ")
	if e != nil {
		return e
	}
	if bound, err := persistBoundControlPlaneState(s, d); bound {
		return err
	}
	return writeAtomic(s.path, append(d, '\n'), 0600)
}

// save persists a complete desired worker state and restores the in-memory
// value if the write fails.
func (s *WorkerStore) save(key string, worker Worker) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.values[key]
	s.values[key] = worker
	if err := s.persistLocked(); err != nil {
		if existed {
			s.values[key] = previous
		} else {
			delete(s.values, key)
		}
		return err
	}
	return nil
}

// remove persists removal of a desired worker and restores the exact previous
// value if durable state cannot be updated.
func (s *WorkerStore) remove(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.values[key]
	delete(s.values, key)
	if err := s.persistLocked(); err != nil {
		if existed {
			s.values[key] = previous
		}
		return err
	}
	return nil
}
func validWorkerName(v string) bool {
	if v == "" || len(v) > 32 {
		return false
	}
	for _, c := range v {
		if !(c == '-' || c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func (a *App) workers(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/workers/"), "/"), "/")
	if len(parts) < 1 || safeUser(parts[0]) == "" {
		http.Error(w, "invalid or inaccessible site", 403)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, parts[0], "invalid or inaccessible site", 403); !ok {
		return
	}
	site := parts[0]
	name := ""
	if len(parts) > 1 {
		name = parts[1]
	}
	if a.Workers == nil {
		http.Error(w, "worker state unavailable", 503)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		a.Workers.mu.RLock()
		list := []Worker{}
		for _, v := range a.Workers.values {
			if v.Site == site && !v.Deleted {
				list = append(list, v)
			}
		}
		a.Workers.mu.RUnlock()
		writeJSON(w, 200, map[string]any{"workers": list})
		return
	}
	if len(parts) == 3 && r.Method == http.MethodPost {
		if !a.Auth.CSRF(r) || (parts[2] != "start" && parts[2] != "stop" && parts[2] != "restart") {
			http.Error(w, "invalid worker action", 422)
			return
		}
		releaseUnlock := a.siteOperations.Acquire(site)
		defer releaseUnlock()
		if err := runHelperCommandWithTimeout(r.Context(), a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "worker-"+parts[2], site, name); err != nil {
			http.Error(w, "worker action failed", 502)
			return
		}
		_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "worker."+parts[2], site, name)
		writeJSON(w, 202, map[string]string{"site": site, "name": name, "action": parts[2]})
		return
	}
	if len(parts) != 2 {
		http.Error(w, "invalid worker", 422)
		return
	}
	name = parts[1]
	if r.Method == http.MethodDelete {
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		releaseUnlock := a.siteOperations.Acquire(site)
		defer releaseUnlock()
		key := site + "/" + name
		a.Workers.mu.RLock()
		previous := a.Workers.values[key]
		a.Workers.mu.RUnlock()
		worker := previous
		worker.Site, worker.Name = site, name
		worker.State, worker.LastError, worker.Deleted = "pending", "", true
		e := a.Workers.save(key, worker)
		if e != nil {
			http.Error(w, "worker state could not be saved", 503)
			return
		}
		if e := a.applyWorker(r.Context(), worker); e != nil {
			a.recordWorkerError(key, e)
			http.Error(w, "worker removal is pending reconciliation", http.StatusBadGateway)
			return
		}
		e = a.Workers.remove(key)
		if e != nil {
			http.Error(w, "worker removed but state cleanup is pending", 503)
			return
		}
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodPut || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var input Worker
	if e := decodeJSON(w, r, 4096, &input); e != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input.Site = site
	input.Name = name
	input.State, input.LastError, input.Deleted = "pending", "", false
	if !validWorkerName(name) || !workerTypes[input.Type] || input.Processes < 1 || input.Processes > 64 || input.MemoryMB < 64 || input.MemoryMB > 65536 || input.Retries < 0 || input.Retries > 20 {
		http.Error(w, "invalid worker definition", 422)
		return
	}
	input.Root = filepath.Join(a.Config.WebRoot, "sites", site, "public")
	if e := ensureInside(a.Config.WebRoot, input.Root); e != nil {
		http.Error(w, "invalid worker root", 422)
		return
	}
	releaseUnlock := a.siteOperations.Acquire(site)
	defer releaseUnlock()
	key := site + "/" + name
	e := a.Workers.save(key, input)
	if e != nil {
		http.Error(w, "worker state could not be saved", 503)
		return
	}
	if e := a.applyWorker(r.Context(), input); e != nil {
		a.recordWorkerError(key, e)
		http.Error(w, "worker is pending reconciliation", 502)
		return
	}
	input.State, input.LastError = "applied", ""
	e = a.Workers.save(key, input)
	if e != nil {
		a.recordWorkerError(key, e)
		http.Error(w, "worker applied but state update is pending", 503)
		return
	}
	_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "worker.updated", site, name)
	writeJSON(w, 202, input)
}

func (a *App) applyWorker(ctx context.Context, worker Worker) error {
	if worker.Deleted {
		return runHelperCommandWithTimeout(ctx, a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "worker-delete", worker.Site, worker.Name)
	}
	return runHelperCommandWithTimeout(ctx, a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "worker-apply", worker.Site, worker.Name, worker.Type, worker.Root, strconv.Itoa(worker.Processes), strconv.Itoa(worker.MemoryMB), strconv.Itoa(worker.Retries))
}

func (a *App) recordWorkerError(key string, applyErr error) {
	a.Workers.mu.RLock()
	worker, ok := a.Workers.values[key]
	a.Workers.mu.RUnlock()
	if !ok {
		return
	}
	worker.State = "pending"
	worker.LastError = applyErr.Error()
	_ = a.Workers.save(key, worker)
}

func (a *App) reconcileWorkers(ctx context.Context) (reconciled []string, failed map[string]string) {
	failed = map[string]string{}
	if a.Workers == nil {
		return nil, failed
	}
	a.Workers.mu.RLock()
	pending := make([]Worker, 0)
	for _, worker := range a.Workers.values {
		if worker.State == "pending" || worker.Deleted {
			pending = append(pending, worker)
		}
	}
	a.Workers.mu.RUnlock()
	for _, worker := range pending {
		key := worker.Site + "/" + worker.Name
		releaseUnlock := a.siteOperations.Acquire(worker.Site)
		if err := a.applyWorker(ctx, worker); err != nil {
			failed[key] = err.Error()
			a.recordWorkerError(key, err)
			releaseUnlock()
			continue
		}
		if worker.Deleted {
			if err := a.Workers.remove(key); err != nil {
				failed[key] = "state persistence failed"
				a.recordWorkerError(key, errors.New("state persistence failed"))
				releaseUnlock()
				continue
			}
		} else {
			worker.State, worker.LastError = "applied", ""
			if err := a.Workers.save(key, worker); err != nil {
				failed[key] = "state persistence failed"
				a.recordWorkerError(key, errors.New("state persistence failed"))
				releaseUnlock()
				continue
			}
		}
		reconciled = append(reconciled, key)
		releaseUnlock()
	}
	return reconciled, failed
}
