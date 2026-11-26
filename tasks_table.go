package stepanel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Scheduled tasks live in scheduled_tasks (one row per task with a revision)
// with their last run output and execution history in child tables, instead
// of the whole-object "tasks" state blob, which every task run rewrote in
// full. TaskStore keeps its map and lock; persistence is a keyedRowTable.

const scheduledTaskTables = `CREATE TABLE IF NOT EXISTS scheduled_tasks (
	task_key TEXT PRIMARY KEY,
	site TEXT NOT NULL,
	name TEXT NOT NULL,
	runtime TEXT NOT NULL DEFAULT '',
	command TEXT NOT NULL DEFAULT '',
	on_calendar TEXT NOT NULL DEFAULT '',
	timeout_sec INTEGER NOT NULL DEFAULT 0,
	enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
	min_interval_seconds INTEGER NOT NULL DEFAULT 0,
	max_concurrent_runs INTEGER NOT NULL DEFAULT 0,
	missed_run_policy TEXT NOT NULL DEFAULT '',
	cpu_percent INTEGER NOT NULL DEFAULT 0,
	memory_mb INTEGER NOT NULL DEFAULT 0,
	tasks_max INTEGER NOT NULL DEFAULT 0,
	notify_webhook TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
	last_run_at INTEGER NOT NULL DEFAULT 0,
	last_run_exit_code INTEGER NOT NULL DEFAULT 0,
	consecutive_failures INTEGER NOT NULL DEFAULT 0,
	auto_disabled_at INTEGER NOT NULL DEFAULT 0,
	current_run_count INTEGER NOT NULL DEFAULT 0,
	revision INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS scheduled_task_output (
	task_key TEXT NOT NULL REFERENCES scheduled_tasks(task_key) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	line TEXT NOT NULL,
	PRIMARY KEY (task_key, position)
);
CREATE TABLE IF NOT EXISTS scheduled_task_executions (
	task_key TEXT NOT NULL REFERENCES scheduled_tasks(task_key) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	started_at INTEGER NOT NULL,
	duration_seconds INTEGER NOT NULL,
	result TEXT NOT NULL,
	exit_code TEXT NOT NULL DEFAULT '',
	exit_status TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (task_key, position)
)`

const scheduledTaskColumns = `site, name, runtime, command, on_calendar, timeout_sec, enabled, min_interval_seconds, max_concurrent_runs, missed_run_policy, cpu_percent, memory_mb, tasks_max, notify_webhook, state, last_error, deleted, last_run_at, last_run_exit_code, consecutive_failures, auto_disabled_at, current_run_count`

func scheduledTaskArgs(t ScheduledTask) []any {
	return []any{t.Site, t.Name, t.Runtime, t.Command, t.OnCalendar, t.TimeoutSec, boolInt(t.Enabled), t.MinIntervalSeconds, t.MaxConcurrentRuns, t.MissedRunPolicy, t.CPUPercent, t.MemoryMB, t.TasksMax, t.NotifyWebhook, t.State, t.LastError, boolInt(t.Deleted), t.LastRunAt, t.LastRunExitCode, t.ConsecutiveFailures, t.AutoDisabledAt, t.CurrentRunCount}
}

// scheduledTaskEqual compares tasks field by field; nil and empty lists are
// equal (both are omitted when encoded).
func scheduledTaskEqual(a, b ScheduledTask) bool {
	if len(a.LastRunOutput) != len(b.LastRunOutput) || len(a.Executions) != len(b.Executions) {
		return false
	}
	for i := range a.LastRunOutput {
		if a.LastRunOutput[i] != b.LastRunOutput[i] {
			return false
		}
	}
	for i := range a.Executions {
		if a.Executions[i] != b.Executions[i] {
			return false
		}
	}
	left, right := scheduledTaskArgs(a), scheduledTaskArgs(b)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func cloneScheduledTask(t ScheduledTask) ScheduledTask {
	if t.LastRunOutput != nil {
		t.LastRunOutput = append([]string{}, t.LastRunOutput...)
	}
	if t.Executions != nil {
		t.Executions = append([]TaskExecution{}, t.Executions...)
	}
	return t
}

func insertScheduledTaskChildren(ctx context.Context, tx *sql.Tx, key string, t ScheduledTask) error {
	for position, line := range t.LastRunOutput {
		if _, err := tx.ExecContext(ctx, `INSERT INTO scheduled_task_output (task_key, position, line) VALUES (?, ?, ?)`, key, position, line); err != nil {
			return err
		}
	}
	for position, run := range t.Executions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO scheduled_task_executions (task_key, position, started_at, duration_seconds, result, exit_code, exit_status) VALUES (?, ?, ?, ?, ?, ?, ?)`, key, position, run.StartedAt, run.DurationSec, run.Result, run.ExitCode, run.ExitStatus); err != nil {
			return err
		}
	}
	return nil
}

const scheduledTaskInsert = `INSERT INTO scheduled_tasks (task_key, ` + scheduledTaskColumns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

var scheduledTaskRows = keyedRowTable[ScheduledTask]{
	name:  "scheduled tasks",
	equal: scheduledTaskEqual,
	clone: cloneScheduledTask,
	load:  loadScheduledTasks,
	insert: func(ctx context.Context, tx *sql.Tx, key string, t ScheduledTask) (bool, error) {
		applied, err := oneRowAffected(tx.ExecContext(ctx, scheduledTaskInsert+` ON CONFLICT(task_key) DO NOTHING`, append([]any{key}, scheduledTaskArgs(t)...)...))
		if err != nil || !applied {
			return applied, err
		}
		return true, insertScheduledTaskChildren(ctx, tx, key, t)
	},
	update: func(ctx context.Context, tx *sql.Tx, key string, t ScheduledTask, revision int64) (bool, error) {
		applied, err := oneRowAffected(tx.ExecContext(ctx, `UPDATE scheduled_tasks SET site = ?, name = ?, runtime = ?, command = ?, on_calendar = ?, timeout_sec = ?, enabled = ?, min_interval_seconds = ?, max_concurrent_runs = ?, missed_run_policy = ?, cpu_percent = ?, memory_mb = ?, tasks_max = ?, notify_webhook = ?, state = ?, last_error = ?, deleted = ?, last_run_at = ?, last_run_exit_code = ?, consecutive_failures = ?, auto_disabled_at = ?, current_run_count = ?, revision = revision + 1 WHERE task_key = ? AND revision = ?`, append(scheduledTaskArgs(t), key, revision)...))
		if err != nil || !applied {
			return applied, err
		}
		for _, table := range []string{"scheduled_task_output", "scheduled_task_executions"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE task_key = ?`, key); err != nil {
				return false, err
			}
		}
		return true, insertScheduledTaskChildren(ctx, tx, key, t)
	},
	remove: func(ctx context.Context, tx *sql.Tx, key string, revision int64) (bool, error) {
		// Output and execution rows go with it (ON DELETE CASCADE).
		return oneRowAffected(tx.ExecContext(ctx, `DELETE FROM scheduled_tasks WHERE task_key = ? AND revision = ?`, key, revision))
	},
}

func loadScheduledTasks(ctx context.Context, db *sql.DB) (map[string]ScheduledTask, map[string]int64, error) {
	values := map[string]ScheduledTask{}
	revisions := map[string]int64{}
	rows, err := db.QueryContext(ctx, `SELECT task_key, revision, `+scheduledTaskColumns+` FROM scheduled_tasks`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var key string
		var revision int64
		var t ScheduledTask
		var enabled, deleted int
		if err := rows.Scan(&key, &revision, &t.Site, &t.Name, &t.Runtime, &t.Command, &t.OnCalendar, &t.TimeoutSec, &enabled, &t.MinIntervalSeconds, &t.MaxConcurrentRuns, &t.MissedRunPolicy, &t.CPUPercent, &t.MemoryMB, &t.TasksMax, &t.NotifyWebhook, &t.State, &t.LastError, &deleted, &t.LastRunAt, &t.LastRunExitCode, &t.ConsecutiveFailures, &t.AutoDisabledAt, &t.CurrentRunCount); err != nil {
			_ = rows.Close() // best-effort: the scan error is returned
			return nil, nil, err
		}
		t.Enabled, t.Deleted = enabled == 1, deleted == 1
		values[key] = t
		revisions[key] = revision
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	output, err := db.QueryContext(ctx, `SELECT task_key, line FROM scheduled_task_output ORDER BY task_key, position`)
	if err != nil {
		return nil, nil, err
	}
	for output.Next() {
		var key, line string
		if err := output.Scan(&key, &line); err != nil {
			_ = output.Close() // best-effort: the scan error is returned
			return nil, nil, err
		}
		t, ok := values[key]
		if !ok {
			_ = output.Close() // best-effort: the integrity error is returned
			return nil, nil, fmt.Errorf("task output for %s has no task row", key)
		}
		t.LastRunOutput = append(t.LastRunOutput, line)
		values[key] = t
	}
	if err := output.Close(); err != nil {
		return nil, nil, err
	}
	executions, err := db.QueryContext(ctx, `SELECT task_key, started_at, duration_seconds, result, exit_code, exit_status FROM scheduled_task_executions ORDER BY task_key, position`)
	if err != nil {
		return nil, nil, err
	}
	defer executions.Close()
	for executions.Next() {
		var key string
		var run TaskExecution
		if err := executions.Scan(&key, &run.StartedAt, &run.DurationSec, &run.Result, &run.ExitCode, &run.ExitStatus); err != nil {
			return nil, nil, err
		}
		t, ok := values[key]
		if !ok {
			return nil, nil, fmt.Errorf("task execution for %s has no task row", key)
		}
		t.Executions = append(t.Executions, run)
		values[key] = t
	}
	return values, revisions, executions.Err()
}

// migrateScheduledTasksBlob moves the "tasks" state blob, if any, into the
// scheduled task tables and deletes it.
func migrateScheduledTasksBlob(tx *sql.Tx) error {
	if _, err := tx.Exec(scheduledTaskTables); err != nil {
		return err
	}
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM state_blobs WHERE name = 'tasks'`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read scheduled task state: %w", err)
	}
	var tasks map[string]ScheduledTask
	if err := json.Unmarshal(payload, &tasks); err != nil {
		return fmt.Errorf("decode scheduled task state: %w", err)
	}
	ctx := context.Background()
	for key, t := range tasks {
		if _, err := tx.Exec(scheduledTaskInsert, append([]any{key}, scheduledTaskArgs(t)...)...); err != nil {
			return fmt.Errorf("migrate scheduled task %s: %w", key, err)
		}
		if err := insertScheduledTaskChildren(ctx, tx, key, t); err != nil {
			return fmt.Errorf("migrate history of scheduled task %s: %w", key, err)
		}
	}
	_, err = tx.Exec(`DELETE FROM state_blobs WHERE name = 'tasks'`)
	return err
}

// bindScheduledTasks makes the scheduled task tables the store's persistence.
func bindScheduledTasks(s *TaskStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = &keyedRowState[ScheduledTask]{}
	}
	return scheduledTaskRows.bind(s.rows, db, s.values)
}
