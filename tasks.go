package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
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
	// Phase 1 safeguards
	LastRunAt           int64    `json:"last_run_at,omitempty"`          // Unix timestamp of last execution
	LastRunExitCode     int      `json:"last_run_exit_code,omitempty"`   // 0 = success, >0 = failure
	LastRunOutput       []string `json:"last_run_output,omitempty"`      // Last N lines of stdout/stderr
	ConsecutiveFailures int      `json:"consecutive_failures,omitempty"` // Count failures for auto-disable
	AutoDisabledAt      int64    `json:"auto_disabled_at,omitempty"`     // When task was auto-disabled
	// Phase 2 safeguards
	NotifyEmail        string `json:"notify_email,omitempty"`         // Email for failure notifications
	MinIntervalSeconds int    `json:"min_interval_seconds,omitempty"` // Rate limiting: min seconds between runs
	MaxConcurrentRuns  int    `json:"max_concurrent_runs,omitempty"`  // Concurrency limit (default 1)
	CurrentRunCount    int    `json:"current_run_count,omitempty"`    // Currently running instances
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

const (
	// Phase 1: Output and execution safeguards
	maxTaskOutputLines      = 100             // Keep last 100 lines of output
	maxTaskOutputSize       = 1024 * 1024     // 1MB max output
	taskTimeoutDefault      = 5 * time.Minute // 5-minute execution limit
	autoDisableFailureCount = 10              // Disable after 10 consecutive failures
)

// recordTaskExecution updates task with execution results
func (s *TaskStore) recordTaskExecution(key string, exitCode int, output []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, exists := s.values[key]
	if !exists {
		return errors.New("task not found")
	}

	task.LastRunAt = time.Now().Unix()
	task.LastRunExitCode = exitCode
	task.LastRunOutput = output

	// Track consecutive failures for auto-disable
	if exitCode == 0 {
		task.ConsecutiveFailures = 0
		task.LastError = ""
	} else {
		task.ConsecutiveFailures++
		task.LastError = "task exited with code " + strconv.Itoa(exitCode)

		// Auto-disable after too many consecutive failures
		if task.ConsecutiveFailures >= autoDisableFailureCount {
			task.Enabled = false
			task.AutoDisabledAt = time.Now().Unix()
			task.LastError = "auto-disabled after " + strconv.Itoa(autoDisableFailureCount) + " consecutive failures"
		}
	}

	s.values[key] = task
	return s.persistLocked()
}

// captureTaskOutput captures and limits output from task execution
func captureTaskOutput(output string) []string {
	lines := strings.Split(output, "\n")

	// Keep only last N lines
	if len(lines) > maxTaskOutputLines {
		lines = lines[len(lines)-maxTaskOutputLines:]
	}

	// Ensure total size doesn't exceed limit
	var result []string
	totalSize := 0
	for i := len(lines) - 1; i >= 0; i-- {
		lineSize := len(lines[i])
		if totalSize+lineSize > maxTaskOutputSize {
			break
		}
		result = append([]string{lines[i]}, result...)
		totalSize += lineSize
	}

	return result
}

// killTask stops a running scheduled task via systemd
func (a *App) killTask(site, name string) error {
	if a.Config.TaskCtl == "" {
		return errors.New("task control helper not configured")
	}
	releaseUnlock := a.siteOperations.Acquire(site)
	defer releaseUnlock()
	return runHelperCommandWithTimeout(context.Background(), a.Config, taskTimeoutDefault, a.Config.TaskCtl, "kill", site, name)
}

// canExecuteTask checks if task can run based on rate limiting and concurrency limits
func (s *TaskStore) canExecuteTask(key string) bool {
	s.mu.RLock()
	task, exists := s.values[key]
	s.mu.RUnlock()

	if !exists || !task.Enabled {
		return false
	}

	// Check concurrent run limit (default 1)
	maxConcurrent := task.MaxConcurrentRuns
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if task.CurrentRunCount >= maxConcurrent {
		return false
	}

	// Check rate limit (min interval between runs)
	if task.MinIntervalSeconds > 0 && task.LastRunAt > 0 {
		timeSinceLastRun := time.Now().Unix() - task.LastRunAt
		if timeSinceLastRun < int64(task.MinIntervalSeconds) {
			return false
		}
	}

	return true
}

// incrementTaskRunCount increments the concurrent run counter
func (s *TaskStore) incrementTaskRunCount(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, exists := s.values[key]
	if !exists {
		return errors.New("task not found")
	}

	task.CurrentRunCount++
	s.values[key] = task
	return s.persistLocked()
}

// decrementTaskRunCount decrements the concurrent run counter
func (s *TaskStore) decrementTaskRunCount(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, exists := s.values[key]
	if !exists {
		return errors.New("task not found")
	}

	if task.CurrentRunCount > 0 {
		task.CurrentRunCount--
	}
	s.values[key] = task
	return s.persistLocked()
}

// sendTaskFailureNotification queues failure notification for task
// Phase 2: External email service integration (SendGrid, AWS SES, etc.)
func (a *App) sendTaskFailureNotification(site, name string, task ScheduledTask, exitCode int, output []string) {
	if task.NotifyEmail == "" {
		return // notifications disabled
	}

	if exitCode == 0 {
		return // only notify on failure
	}

	details := fmt.Sprintf(
		"exit_code=%d failures=%d last_run=%s",
		exitCode,
		task.ConsecutiveFailures,
		time.Unix(task.LastRunAt, 0).Format(time.RFC3339),
	)

	// Log notification event to audit trail
	// Production implementation would send email via external service
	_ = ShouldAudit(a.Config.AuditLog, "system", "task.failure.notified", site+"/"+name, details)
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
	if len(parts) < 2 || len(parts) > 3 || !validWorkerName(parts[1]) {
		http.Error(w, "invalid task", 422)
		return
	}
	name := parts[1]

	// Handle kill/cancel endpoint: POST /api/tasks/{site}/{name}/kill
	if len(parts) == 3 && parts[2] == "kill" {
		if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", 403)
			return
		}
		if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
			http.Error(w, "insufficient token scope for task operations", http.StatusForbidden)
			return
		}
		if err := a.killTask(site, name); err != nil {
			http.Error(w, "could not kill task: "+err.Error(), 502)
			return
		}
		_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "task.killed", site, name)
		writeJSON(w, 202, map[string]string{"site": site, "name": name, "action": "kill"})
		return
	}

	if len(parts) != 2 {
		http.Error(w, "invalid task", 422)
		return
	}

	// Handle GET for task execution history
	if r.Method == http.MethodGet {
		key := site + "/" + name
		a.Tasks.mu.RLock()
		task, exists := a.Tasks.values[key]
		a.Tasks.mu.RUnlock()
		if !exists || task.Deleted {
			http.Error(w, "task not found", 404)
			return
		}
		response := map[string]any{
			"site":                 task.Site,
			"name":                 task.Name,
			"last_run_at":          task.LastRunAt,
			"last_run_exit_code":   task.LastRunExitCode,
			"last_run_output":      task.LastRunOutput,
			"consecutive_failures": task.ConsecutiveFailures,
			"auto_disabled_at":     task.AutoDisabledAt,
			"enabled":              task.Enabled,
			"last_error":           task.LastError,
		}
		writeJSON(w, 200, response)
		return
	}

	if r.Method == http.MethodDelete {
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
			http.Error(w, "insufficient token scope for task operations", http.StatusForbidden)
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
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for task operations", http.StatusForbidden)
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

	// Apply defaults for fields the browser form may not send
	if input.MaxConcurrentRuns == 0 {
		input.MaxConcurrentRuns = 1  // Default: one concurrent run at a time
	}

	input.Command, input.OnCalendar = strings.TrimSpace(input.Command), strings.TrimSpace(input.OnCalendar)
	input.NotifyEmail = strings.TrimSpace(input.NotifyEmail)
	if !validTaskRuntime(input.Runtime) || input.Command == "" || len(input.Command) > 1024 || strings.ContainsAny(input.Command, "\x00\r\n") || input.OnCalendar == "" || len(input.OnCalendar) > 128 || strings.ContainsAny(input.OnCalendar, "\x00\r\n") || input.TimeoutSec < 1 || input.TimeoutSec > 86400 {
		http.Error(w, "invalid scheduled task definition", 422)
		return
	}
	// Validate Phase 2 fields
	if input.MinIntervalSeconds < 0 || input.MinIntervalSeconds > 86400 {
		http.Error(w, "min_interval_seconds must be 0-86400", 422)
		return
	}
	if input.MaxConcurrentRuns < 1 || input.MaxConcurrentRuns > 100 {
		http.Error(w, "max_concurrent_runs must be 1-100", 422)
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
	// Use standard Base64 with padding for cross-platform compatibility
	// RawStdEncoding (without padding) fails on GNU base64 -d for commands needing padding
	encodedCommand := base64.StdEncoding.EncodeToString([]byte(task.Command))
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
