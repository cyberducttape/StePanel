package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

// ScheduledTask is intentionally a systemd-timer definition, rather than a
// writable crontab fragment. Commands execute as the isolated site identity;
// the helper applies time, process and filesystem restrictions consistently.
type ScheduledTask struct {
	Site       string `json:"site"`
	Name       string `json:"name"`
	Runtime    string `json:"runtime"`
	Command    string `json:"command"`
	OnCalendar string `json:"on_calendar"`
	TimeoutSec int    `json:"timeout_sec"`
	Enabled    bool   `json:"enabled"`
	State      string `json:"state,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
}

type TaskStore struct {
	mu     sync.RWMutex
	path   string
	values map[string]ScheduledTask
}

func OpenTaskStore(path string) (*TaskStore, error) {
	s := &TaskStore{path: path, values: map[string]ScheduledTask{}}
	d, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(d, &s.values); err != nil {
		return nil, err
	}
	for key, task := range s.values {
		if task.State == "" {
			task.State = "applied"
			s.values[key] = task
		}
	}
	return s, nil
}
func (s *TaskStore) persistLocked() error {
	d, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(s, d); bound {
		return err
	}
	return writeAtomic(s.path, append(d, '\n'), 0600)
}

// save persists a complete desired task state and restores the in-memory
// value if durability fails. This keeps the running control plane aligned with
// the state that will be loaded after a restart.
func (s *TaskStore) save(key string, task ScheduledTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.values[key]
	s.values[key] = task
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
func validTaskRuntime(v string) bool {
	return v == "php" || v == "node" || v == "python" || v == "shell"
}

func (a *App) tasks(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/"), "/")
	if len(parts) < 1 || safeUser(parts[0]) == "" {
		http.Error(w, "invalid or inaccessible site", 403)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, parts[0], "invalid or inaccessible site", 403); !ok {
		return
	}
	if a.Tasks == nil {
		http.Error(w, "invalid or inaccessible site", 403)
		return
	}
	site := parts[0]
	if len(parts) == 1 && r.Method == http.MethodGet {
		a.Tasks.mu.RLock()
		result := []ScheduledTask{}
		for _, task := range a.Tasks.values {
			if task.Site == site && !task.Deleted {
				result = append(result, task)
			}
		}
		a.Tasks.mu.RUnlock()
		writeJSON(w, 200, map[string]any{"tasks": result})
		return
	}
	if len(parts) != 2 || !validWorkerName(parts[1]) {
		http.Error(w, "invalid task", 422)
		return
	}
	name := parts[1]
	if r.Method == http.MethodDelete {
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		releaseUnlock := a.siteOperations.Acquire(site)
		defer releaseUnlock()
		key := site + "/" + name
		a.Tasks.mu.RLock()
		task := a.Tasks.values[key]
		a.Tasks.mu.RUnlock()
		task.Site, task.Name, task.State, task.Deleted, task.LastError = site, name, "pending", true, ""
		err := a.Tasks.save(key, task)
		if err != nil {
			http.Error(w, "could not persist task state", 503)
			return
		}
		if err := a.applyTask(r.Context(), task); err != nil {
			a.recordTaskError(key, err)
			http.Error(w, "scheduled task removal is pending reconciliation", 502)
			return
		}
		a.Tasks.mu.Lock()
		err = a.finalizeTaskDeletionLocked(key, task)
		a.Tasks.mu.Unlock()
		if err != nil {
			http.Error(w, "scheduled task removed but state cleanup is pending", 503)
			return
		}
		_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "task.deleted", site, name)
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodPut || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var input ScheduledTask
	if err := decodeJSON(w, r, 8192, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input.Site, input.Name = site, name
	input.State = "pending"
	input.LastError = ""
	input.Deleted = false
	input.Command, input.OnCalendar = strings.TrimSpace(input.Command), strings.TrimSpace(input.OnCalendar)
	if !validTaskRuntime(input.Runtime) || input.Command == "" || len(input.Command) > 1024 || strings.ContainsAny(input.Command, "\x00\r\n") || input.OnCalendar == "" || len(input.OnCalendar) > 128 || strings.ContainsAny(input.OnCalendar, "\x00\r\n") || input.TimeoutSec < 1 || input.TimeoutSec > 86400 {
		http.Error(w, "invalid scheduled task definition", 422)
		return
	}
	releaseUnlock := a.siteOperations.Acquire(site)
	defer releaseUnlock()
	key := site + "/" + name
	err := a.Tasks.save(key, input)
	if err != nil {
		http.Error(w, "could not persist task state", 503)
		return
	}
	if err := a.applyTask(r.Context(), input); err != nil {
		a.recordTaskError(key, err)
		http.Error(w, "scheduled task is pending reconciliation", 502)
		return
	}
	input.State, input.LastError = "applied", ""
	err = a.Tasks.save(key, input)
	if err != nil {
		a.recordTaskError(key, err)
		http.Error(w, "scheduled task applied but state update is pending", 503)
		return
	}
	_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "task.updated", site, name)
	writeJSON(w, 202, input)
}

// finalizeTaskDeletionLocked removes a task only when the resulting desired
// state is durable. Callers must hold Tasks.mu. Restoring the map entry on a
// pre-commit write failure keeps the running process aligned with the state
// that will be loaded on the next startup.
func (a *App) finalizeTaskDeletionLocked(key string, task ScheduledTask) error {
	delete(a.Tasks.values, key)
	if err := a.Tasks.persistLocked(); err != nil {
		a.Tasks.values[key] = task
		return err
	}
	return nil
}

func (a *App) applyTask(ctx context.Context, task ScheduledTask) error {
	if task.Deleted {
		return runHelperCommandWithTimeout(ctx, a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "task-delete", task.Site, task.Name)
	}
	encodedCommand := base64.RawStdEncoding.EncodeToString([]byte(task.Command))
	// Keep task limits aligned with the site's desired resource profile. The
	// helper retains a conservative fallback for sites that have no profile.
	args := []string{"task-apply", task.Site, task.Name, task.Runtime, task.OnCalendar, stringBool(task.Enabled), itoa(task.TimeoutSec), encodedCommand}
	if a.Resources != nil {
		a.Resources.mu.RLock()
		profile, configured := a.Resources.values[task.Site]
		a.Resources.mu.RUnlock()
		if configured {
			args = append(args, strconv.Itoa(profile.CPUPercent), strconv.Itoa(profile.MemoryMB), strconv.Itoa(profile.TasksMax))
		}
	}
	return runHelperCommandWithTimeout(ctx, a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, args...)
}

func (a *App) recordTaskError(key string, applyErr error) {
	a.Tasks.mu.RLock()
	task, ok := a.Tasks.values[key]
	a.Tasks.mu.RUnlock()
	if !ok {
		return
	}
	task.State = "pending"
	task.LastError = applyErr.Error()
	_ = a.Tasks.save(key, task)
}

func (a *App) reconcileTasks(ctx context.Context) (reconciled []string, failed map[string]string) {
	failed = map[string]string{}
	a.Tasks.mu.RLock()
	pending := make([]ScheduledTask, 0)
	for _, task := range a.Tasks.values {
		if task.State == "pending" || task.Deleted {
			pending = append(pending, task)
		}
	}
	a.Tasks.mu.RUnlock()
	for _, task := range pending {
		key := task.Site + "/" + task.Name
		releaseUnlock := a.siteOperations.Acquire(task.Site)
		if err := a.applyTask(ctx, task); err != nil {
			failed[key] = err.Error()
			a.recordTaskError(key, err)
			releaseUnlock()
			continue
		}
		if task.Deleted {
			a.Tasks.mu.Lock()
			err := a.finalizeTaskDeletionLocked(key, task)
			a.Tasks.mu.Unlock()
			if err != nil {
				failed[key] = "state persistence failed"
				a.recordTaskError(key, errors.New("state persistence failed"))
				releaseUnlock()
				continue
			}
		} else {
			task.State, task.LastError = "applied", ""
			if err := a.Tasks.save(key, task); err != nil {
				failed[key] = "state persistence failed"
				a.recordTaskError(key, errors.New("state persistence failed"))
				releaseUnlock()
				continue
			}
		}
		reconciled = append(reconciled, key)
		releaseUnlock()
	}
	return reconciled, failed
}

func (a *App) reconcileTasksHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}
	reconciled, failed := a.reconcileTasks(r.Context())
	_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "tasks.reconciled", "tasks", strings.Join(reconciled, ","))
	writeJSON(w, http.StatusOK, map[string]any{"reconciled": reconciled, "failed": failed})
}

func stringBool(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
