package metadata

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TaskIndex manages SQLite-backed indexing of scheduled task metadata
type TaskIndex struct {
	db *sql.DB
}

// TaskEntry represents indexed task metadata
type TaskEntry struct {
	Site                string
	Name                string
	Runtime             string
	Enabled             bool
	LastRunAt           time.Time
	LastRunExitCode     int
	ConsecutiveFailures int
	AutoDisabledAt      *time.Time
	NotifyEmail         string
	MinIntervalSeconds  int
	MaxConcurrentRuns   int
	CurrentRunCount     int
	LastIndexed         time.Time
}

// NewTaskIndex creates or opens the task index
func NewTaskIndex(db *sql.DB) (*TaskIndex, error) {
	if db == nil {
		return nil, errors.New("database connection required")
	}

	idx := &TaskIndex{db: db}
	if err := idx.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize task index schema: %w", err)
	}

	return idx, nil
}

// initSchema creates the task index tables
func (idx *TaskIndex) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS task_index (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		site TEXT NOT NULL,
		task_name TEXT NOT NULL,
		runtime TEXT NOT NULL,
		enabled BOOLEAN DEFAULT 1,
		last_run_at TIMESTAMP,
		last_run_exit_code INTEGER,
		consecutive_failures INTEGER DEFAULT 0,
		auto_disabled_at TIMESTAMP,
		notify_email TEXT,
		min_interval_seconds INTEGER DEFAULT 0,
		max_concurrent_runs INTEGER DEFAULT 1,
		current_run_count INTEGER DEFAULT 0,
		last_indexed TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(site, task_name),
		INDEX idx_site (site),
		INDEX idx_enabled (enabled),
		INDEX idx_failures (consecutive_failures DESC),
		INDEX idx_last_run (last_run_at DESC)
	);

	CREATE TABLE IF NOT EXISTS task_execution_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id INTEGER NOT NULL,
		executed_at TIMESTAMP NOT NULL,
		exit_code INTEGER,
		duration_ms INTEGER,
		output_lines INTEGER,
		FOREIGN KEY (task_id) REFERENCES task_index(id) ON DELETE CASCADE,
		INDEX idx_task_executed (task_id, executed_at DESC)
	);

	CREATE TABLE IF NOT EXISTS task_failure_windows (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id INTEGER NOT NULL,
		failure_count INTEGER,
		first_failure_at TIMESTAMP,
		last_failure_at TIMESTAMP,
		notified_at TIMESTAMP,
		FOREIGN KEY (task_id) REFERENCES task_index(id) ON DELETE CASCADE,
		UNIQUE(task_id)
	);
	`

	_, err := idx.db.Exec(schema)
	return err
}

// AddTask indexes a new task
func (idx *TaskIndex) AddTask(entry TaskEntry) error {
	stmt, err := idx.db.Prepare(`
		INSERT INTO task_index (
			site, task_name, runtime, enabled, last_run_at,
			last_run_exit_code, consecutive_failures, auto_disabled_at,
			notify_email, min_interval_seconds, max_concurrent_runs
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(site, task_name) DO UPDATE SET
			enabled = excluded.enabled,
			last_run_at = excluded.last_run_at,
			last_run_exit_code = excluded.last_run_exit_code,
			consecutive_failures = excluded.consecutive_failures,
			notify_email = excluded.notify_email,
			min_interval_seconds = excluded.min_interval_seconds,
			max_concurrent_runs = excluded.max_concurrent_runs,
			last_indexed = CURRENT_TIMESTAMP
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(
		entry.Site, entry.Name, entry.Runtime, entry.Enabled,
		entry.LastRunAt, entry.LastRunExitCode, entry.ConsecutiveFailures,
		entry.AutoDisabledAt, entry.NotifyEmail, entry.MinIntervalSeconds,
		entry.MaxConcurrentRuns,
	)
	return err
}

// ListTasks returns tasks for a site
func (idx *TaskIndex) ListTasks(site string, enabledOnly bool) ([]TaskEntry, error) {
	query := `
		SELECT site, task_name, runtime, enabled, last_run_at,
		       last_run_exit_code, consecutive_failures, auto_disabled_at,
		       notify_email, min_interval_seconds, max_concurrent_runs,
		       current_run_count, last_indexed
		FROM task_index
		WHERE site = ?
	`
	args := []interface{}{site}

	if enabledOnly {
		query += " AND enabled = 1"
	}

	query += " ORDER BY task_name"

	rows, err := idx.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []TaskEntry
	for rows.Next() {
		var entry TaskEntry
		err := rows.Scan(
			&entry.Site, &entry.Name, &entry.Runtime, &entry.Enabled,
			&entry.LastRunAt, &entry.LastRunExitCode, &entry.ConsecutiveFailures,
			&entry.AutoDisabledAt, &entry.NotifyEmail, &entry.MinIntervalSeconds,
			&entry.MaxConcurrentRuns, &entry.CurrentRunCount, &entry.LastIndexed,
		)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, entry)
	}

	return tasks, rows.Err()
}

// RecordExecution logs a task execution
func (idx *TaskIndex) RecordExecution(site, taskName string, exitCode int, durationMs int, outputLines int) error {
	stmt, err := idx.db.Prepare(`
		INSERT INTO task_execution_history (task_id, executed_at, exit_code, duration_ms, output_lines)
		SELECT id, CURRENT_TIMESTAMP, ?, ?, ?
		FROM task_index
		WHERE site = ? AND task_name = ?
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(exitCode, durationMs, outputLines, site, taskName)
	return err
}

// GetExecutionHistory returns recent executions for a task
func (idx *TaskIndex) GetExecutionHistory(site, taskName string, limit int) ([]map[string]interface{}, error) {
	query := `
		SELECT executed_at, exit_code, duration_ms, output_lines
		FROM task_execution_history
		WHERE task_id = (SELECT id FROM task_index WHERE site = ? AND task_name = ?)
		ORDER BY executed_at DESC
		LIMIT ?
	`

	rows, err := idx.db.Query(query, site, taskName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []map[string]interface{}
	for rows.Next() {
		var executedAt time.Time
		var exitCode, durationMs, outputLines int
		err := rows.Scan(&executedAt, &exitCode, &durationMs, &outputLines)
		if err != nil {
			return nil, err
		}

		history = append(history, map[string]interface{}{
			"executed_at":   executedAt,
			"exit_code":     exitCode,
			"duration_ms":   durationMs,
			"output_lines":  outputLines,
		})
	}

	return history, rows.Err()
}

// GetFailingTasks returns tasks with excessive failures
func (idx *TaskIndex) GetFailingTasks(minConsecutiveFailures int) ([]TaskEntry, error) {
	query := `
		SELECT site, task_name, runtime, enabled, last_run_at,
		       last_run_exit_code, consecutive_failures, auto_disabled_at,
		       notify_email, min_interval_seconds, max_concurrent_runs,
		       current_run_count, last_indexed
		FROM task_index
		WHERE consecutive_failures >= ? AND auto_disabled_at IS NULL
		ORDER BY consecutive_failures DESC
	`

	rows, err := idx.db.Query(query, minConsecutiveFailures)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []TaskEntry
	for rows.Next() {
		var entry TaskEntry
		err := rows.Scan(
			&entry.Site, &entry.Name, &entry.Runtime, &entry.Enabled,
			&entry.LastRunAt, &entry.LastRunExitCode, &entry.ConsecutiveFailures,
			&entry.AutoDisabledAt, &entry.NotifyEmail, &entry.MinIntervalSeconds,
			&entry.MaxConcurrentRuns, &entry.CurrentRunCount, &entry.LastIndexed,
		)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, entry)
	}

	return tasks, rows.Err()
}

// CleanupOldExecutions removes execution history older than duration
func (idx *TaskIndex) CleanupOldExecutions(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := idx.db.Exec(
		"DELETE FROM task_execution_history WHERE executed_at < ?",
		cutoff,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
