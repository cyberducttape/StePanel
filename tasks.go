package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ScheduledTask is intentionally a systemd-timer definition, rather than a
// writable crontab fragment. Commands execute as the isolated site identity;
// the helper applies time, process and filesystem restrictions consistently.
type ScheduledTask struct {
	Site                string          `json:"site"`
	Name                string          `json:"name"`
	Runtime             string          `json:"runtime"`
	Command             string          `json:"command"`
	OnCalendar          string          `json:"on_calendar"`
	TimeoutSec          int             `json:"timeout_sec"`
	Enabled             bool            `json:"enabled"`
	MinIntervalSeconds  int             `json:"min_interval_seconds"`
	MaxConcurrentRuns   int             `json:"max_concurrent_runs"`
	MissedRunPolicy     string          `json:"missed_run_policy"`
	CPUPercent          int             `json:"cpu_percent"`
	MemoryMB            int             `json:"memory_mb"`
	TasksMax            int             `json:"tasks_max"`
	NotifyWebhook       string          `json:"notify_webhook,omitempty"`
	State               string          `json:"state,omitempty"`
	LastError           string          `json:"last_error,omitempty"`
	Deleted             bool            `json:"deleted,omitempty"`
	LastRunAt           int64           `json:"last_run_at,omitempty"`
	LastRunExitCode     int             `json:"last_run_exit_code,omitempty"`
	LastRunOutput       []string        `json:"last_run_output,omitempty"`
	ConsecutiveFailures int             `json:"consecutive_failures,omitempty"`
	AutoDisabledAt      int64           `json:"auto_disabled_at,omitempty"`
	CurrentRunCount     int             `json:"current_run_count,omitempty"`
	Executions          []TaskExecution `json:"executions,omitempty"`
}

type TaskExecution struct {
	StartedAt   int64  `json:"started_at"`
	DurationSec int64  `json:"duration_seconds"`
	Result      string `json:"result"`
	ExitCode    string `json:"exit_code"`
	ExitStatus  string `json:"exit_status"`
}

type taskHistorySnapshot struct {
	Executions          []TaskExecution `json:"executions"`
	CurrentRunCount     int             `json:"current_run_count"`
	ConsecutiveFailures int             `json:"consecutive_failures"`
	AutoDisabledAt      int64           `json:"auto_disabled_at"`
	Enabled             bool            `json:"enabled"`
}

func loadTaskHistory(ctx context.Context, cfg Config, site, name string) (taskHistorySnapshot, error) {
	output, err, _ := runAllowlistedHelperOutput(ctx, cfg, nil, cfg.AppCtl, "task-history", site, name)
	if err != nil {
		return taskHistorySnapshot{}, err
	}
	var snapshot taskHistorySnapshot
	if err := json.Unmarshal(output, &snapshot); err != nil {
		return taskHistorySnapshot{}, fmt.Errorf("decode task history: %w", err)
	}
	return snapshot, nil
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

// normalizeSafeguards upgrades persisted task definitions before reconciliation
// so older entries receive the enforced defaults and are republished as units.
func (s *TaskStore) normalizeSafeguards() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	previous := make(map[string]ScheduledTask)
	for key, task := range s.values {
		original := task
		if err := normalizeScheduledTask(&task); err != nil {
			return fmt.Errorf("normalize scheduled task %s: %w", key, err)
		}
		if task.State == "" {
			task.State = "applied"
		}
		if !reflect.DeepEqual(original, task) {
			task.State = "pending"
			previous[key] = original
			s.values[key] = task
			changed = true
		}
	}
	if changed {
		if err := s.persistLocked(); err != nil {
			for key, task := range previous {
				s.values[key] = task
			}
			return err
		}
	}
	return nil
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
	minTaskIntervalSeconds  = 60
	maxTaskIntervalSeconds  = 31536000
	maxTaskHistory          = 20
)

var taskCalendarOccurrence = regexp.MustCompile(`(?m)^\s*(?:Next elapse:|Iteration #[0-9]+:)\s*(.+?) UTC\s*$`)

func normalizeScheduledTask(task *ScheduledTask) error {
	if task.MinIntervalSeconds == 0 {
		task.MinIntervalSeconds = minTaskIntervalSeconds
	}
	if task.MinIntervalSeconds < minTaskIntervalSeconds || task.MinIntervalSeconds > maxTaskIntervalSeconds {
		return fmt.Errorf("minimum interval must be between %d and %d seconds", minTaskIntervalSeconds, maxTaskIntervalSeconds)
	}
	if task.MaxConcurrentRuns == 0 {
		task.MaxConcurrentRuns = 1
	}
	if task.MaxConcurrentRuns != 1 {
		return errors.New("scheduled tasks support exactly one concurrent run")
	}
	if task.MissedRunPolicy == "" {
		task.MissedRunPolicy = "run_once"
	}
	if task.MissedRunPolicy != "run_once" && task.MissedRunPolicy != "skip" {
		return errors.New("missed-run policy must be run_once or skip")
	}
	if task.CPUPercent == 0 {
		task.CPUPercent = 100
	}
	if task.MemoryMB == 0 {
		task.MemoryMB = 1024
	}
	if task.TasksMax == 0 {
		task.TasksMax = 256
	}
	if task.CPUPercent < 25 || task.CPUPercent > 6400 || task.MemoryMB < 64 || task.MemoryMB > 1048576 || task.TasksMax < 16 || task.TasksMax > 100000 {
		return errors.New("task CPU, memory, or process limits are out of range")
	}
	if task.NotifyWebhook != "" && !validTaskWebhook(task.NotifyWebhook) {
		return errors.New("notification webhook must be an HTTPS URL without credentials or control characters")
	}
	return nil
}

func validTaskWebhook(raw string) bool {
	if len(raw) > 2048 || strings.ContainsAny(raw, "\x00\r\n\t '\"\\") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

func validateTaskCalendarInterval(ctx context.Context, calendar string, minimumSeconds int) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	base := time.Now().UTC().Truncate(time.Second)
	var previous time.Time
	for range 32 {
		cmd := exec.CommandContext(ctx, "systemd-analyze", "--base-time=@"+strconv.FormatInt(base.Unix(), 10), "calendar", calendar)
		cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("invalid systemd calendar expression: %s", strings.TrimSpace(string(output)))
		}
		match := taskCalendarOccurrence.FindStringSubmatch(string(output))
		if len(match) < 2 {
			return errors.New("scheduled task calendar must recur")
		}
		occurrence, parseErr := time.Parse("Mon 2006-01-02 15:04:05", strings.TrimSpace(match[1]))
		if parseErr != nil {
			return fmt.Errorf("could not inspect calendar recurrence: %w", parseErr)
		}
		if !previous.IsZero() && int(occurrence.Sub(previous).Seconds()) < minimumSeconds {
			return fmt.Errorf("calendar runs more frequently than the configured minimum interval of %d seconds", minimumSeconds)
		}
		previous = occurrence
		base = occurrence.Add(time.Second)
	}
	return nil
}

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

	return persistMapKeyChange(s.values, key, &task, s.persistLocked)
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

// killTask stops a running scheduled task via systemd.
// Uses stepanel-appctl helper (installed at /usr/local/sbin/stepanel-appctl)
// with task-kill action, invoked through the privileged root wrapper.
func (a *App) killTask(ctx context.Context, site, name string) error {
	if a.Config.AppCtl == "" {
		return errors.New("app control helper not configured")
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(ctx, site)
	if lockErr != nil {
		return fmt.Errorf("acquire task lock: %w", lockErr)
	}
	defer releaseUnlock()
	// Use AppCtl with task-kill action instead of missing TaskCtl helper
	return runHelperCommandWithTimeout(operationCtx, a.Config, 15*time.Second, a.Config.AppCtl, "task-kill", site, name)
}

// Task overlap is prevented by systemd's single active oneshot service per
// task unit. Runtime, resource, recurrence, missed-run, failure, history,
// notification, and manual-stop safeguards are enforced by task-apply and the
// generated unit; do not add a second in-memory run counter as an authority.

func (a *App) tasks(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/"), "/")
	if len(parts) < 1 || safeUser(parts[0]) == "" {
		http.Error(w, "invalid or inaccessible site", 403)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, parts[0], "invalid or inaccessible site", 403); !ok {
		return
	}
	if r.Method == http.MethodGet && !a.requireCustomerScope(w, r, "site:read") {
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
		for i := range result {
			history, err := loadTaskHistory(r.Context(), a.Config, site, result[i].Name)
			if err != nil {
				http.Error(w, "task execution state is unavailable", http.StatusServiceUnavailable)
				return
			}
			result[i].Enabled = history.Enabled
			result[i].CurrentRunCount = history.CurrentRunCount
			result[i].ConsecutiveFailures = history.ConsecutiveFailures
			result[i].AutoDisabledAt = history.AutoDisabledAt
			result[i].Executions = history.Executions
			if len(history.Executions) > 0 {
				result[i].LastRunAt = history.Executions[0].StartedAt
				result[i].LastRunExitCode, _ = strconv.Atoi(history.Executions[0].ExitStatus)
			}
		}
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
		if err := a.killTask(r.Context(), site, name); err != nil {
			http.Error(w, "could not kill task: "+err.Error(), 502)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "task.killed", site, name)
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
		history, err := loadTaskHistory(r.Context(), a.Config, site, name)
		if err != nil {
			http.Error(w, "task execution history is unavailable", http.StatusServiceUnavailable)
			return
		}
		lastRunAt, lastExitCode := int64(0), 0
		if len(history.Executions) > 0 {
			lastRunAt = history.Executions[0].StartedAt
			lastExitCode, _ = strconv.Atoi(history.Executions[0].ExitStatus)
		}
		response := map[string]any{
			"site":                 task.Site,
			"name":                 task.Name,
			"last_run_at":          lastRunAt,
			"last_run_exit_code":   lastExitCode,
			"last_error":           task.LastError,
			"executions":           history.Executions,
			"current_run_count":    history.CurrentRunCount,
			"consecutive_failures": history.ConsecutiveFailures,
			"auto_disabled_at":     history.AutoDisabledAt,
			"enabled":              history.Enabled,
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
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
		if lockErr != nil {
			http.Error(w, "task operation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "task operation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
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
		if err := a.applyTask(operationCtx, task); err != nil {
			a.recordTaskError(key, err)
			http.Error(w, "scheduled task removal is pending reconciliation", 502)
			return
		}
		if err := operationCtx.Err(); err != nil {
			a.recordTaskError(key, err)
			http.Error(w, "scheduled task removal is pending reconciliation", http.StatusConflict)
			return
		}
		a.Tasks.mu.Lock()
		err = a.finalizeTaskDeletionLocked(key, task)
		a.Tasks.mu.Unlock()
		if err != nil {
			http.Error(w, "scheduled task removed but state cleanup is pending", 503)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "task.deleted", site, name)
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
	// Execution telemetry is owned by the privileged task runner. Never accept
	// client-supplied history or counters as part of a definition update.
	input.LastRunAt = 0
	input.LastRunExitCode = 0
	input.LastRunOutput = nil
	input.ConsecutiveFailures = 0
	input.AutoDisabledAt = 0
	input.CurrentRunCount = 0
	input.Executions = nil

	input.Command, input.OnCalendar = strings.TrimSpace(input.Command), strings.TrimSpace(input.OnCalendar)
	if err := normalizeScheduledTask(&input); err != nil {
		http.Error(w, "invalid scheduled task safeguards: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if !validTaskRuntime(input.Runtime) || input.Command == "" || len(input.Command) > 1024 || strings.ContainsAny(input.Command, "\x00\r\n") || input.OnCalendar == "" || len(input.OnCalendar) > 128 || strings.ContainsAny(input.OnCalendar, "\x00\r\n") || input.TimeoutSec < 1 || input.TimeoutSec > 86400 {
		http.Error(w, "invalid scheduled task definition", 422)
		return
	}
	if err := validateTaskCalendarInterval(r.Context(), input.OnCalendar, input.MinIntervalSeconds); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
	if lockErr != nil {
		http.Error(w, "task mutation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "task mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	key := site + "/" + name
	err := a.Tasks.save(key, input)
	if err != nil {
		http.Error(w, "could not persist task state", 503)
		return
	}
	if err := a.applyTask(operationCtx, input); err != nil {
		a.recordTaskError(key, err)
		http.Error(w, "scheduled task is pending reconciliation", 502)
		return
	}
	if err := operationCtx.Err(); err != nil {
		a.recordTaskError(key, err)
		http.Error(w, "scheduled task is pending reconciliation", http.StatusConflict)
		return
	}
	input.State, input.LastError = "applied", ""
	err = a.Tasks.save(key, input)
	if err != nil {
		a.recordTaskError(key, err)
		http.Error(w, "scheduled task applied but state update is pending", 503)
		return
	}
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "task.updated", site, name)
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
	args := []string{"task-apply", task.Site, task.Name, task.Runtime, task.OnCalendar, stringBool(task.Enabled), itoa(task.TimeoutSec), encodedCommand,
		strconv.Itoa(task.MinIntervalSeconds), task.MissedRunPolicy, strconv.Itoa(task.CPUPercent), strconv.Itoa(task.MemoryMB), strconv.Itoa(task.TasksMax), task.NotifyWebhook}
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
	a.SaveTaskState(key, task)
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
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(ctx, task.Site)
		if lockErr != nil {
			failed[key] = lockErr.Error()
			continue
		}
		if err := a.applyTask(operationCtx, task); err != nil {
			failed[key] = err.Error()
			a.recordTaskError(key, err)
			releaseUnlock()
			continue
		}
		if err := operationCtx.Err(); err != nil {
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
			if err := operationCtx.Err(); err != nil {
				a.recordTaskError(key, err)
				failed[key] = err.Error()
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
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "tasks.reconciled", "tasks", strings.Join(reconciled, ","))
	writeJSON(w, http.StatusOK, map[string]any{"reconciled": reconciled, "failed": failed})
}

func stringBool(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
