package stepanel

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Metrics contains the small, dependency-free Prometheus surface exposed by
// StePanel. Keeping the counters in-process makes the control plane observable
// even in minimal installations without a metrics SDK or sidecar.
type Metrics struct {
	restoresStarted     atomic.Uint64
	restoresCompleted   atomic.Uint64
	restoresFailed      atomic.Uint64
	activeRestores      atomic.Int64
	httpRequests        atomic.Uint64
	httpErrors          atomic.Uint64
	httpDurationNanos   atomic.Uint64
	httpStatus          [6]atomic.Uint64
	httpDurationBuckets [12]atomic.Uint64
	stateErrors         [5]atomic.Uint64
	sqliteBusyErrors    atomic.Uint64
	uploadsStaged       atomic.Uint64
	uploadBytes         atomic.Uint64
	uploadRejections    [len(uploadRejectReasons)]atomic.Uint64
	streamsOpened       atomic.Uint64
	streamsRejected     atomic.Uint64
	activeStreams       atomic.Int64
}

// Upload rejection reasons are a bounded label set for
// stepanel_upload_rejections_total.
const (
	uploadRejectCapacity = iota
	uploadRejectTooLarge
	uploadRejectStalled
	uploadRejectLength
	uploadRejectInvalid
	uploadRejectInternal
)

var uploadRejectReasons = [...]string{"capacity", "too_large", "stalled", "length_required", "invalid", "internal"}

var httpDurationBounds = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
var stateMetricCategories = [...]string{"temporary", "persistence", "corruption", "cleanup", "unsupported"}

func (m *Metrics) ObserveStateError(category string) {
	if m == nil {
		return
	}
	for i, candidate := range stateMetricCategories {
		if strings.EqualFold(strings.TrimSpace(category), candidate) {
			m.stateErrors[i].Add(1)
			return
		}
	}
}

func (m *Metrics) ObserveSQLiteError(err error) {
	if m == nil || err == nil {
		return
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "sqlite_busy") || strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") {
		m.sqliteBusyErrors.Add(1)
	}
}

func (m *Metrics) ObserveHTTP(status int, duration time.Duration) {
	if m == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	m.httpRequests.Add(1)
	if status >= 500 {
		m.httpErrors.Add(1)
	}
	m.httpDurationNanos.Add(uint64(duration))
	bucket := status / 100
	if bucket >= 1 && bucket <= 5 {
		m.httpStatus[bucket].Add(1)
	}
	seconds := duration.Seconds()
	for i, bound := range httpDurationBounds {
		if seconds <= bound {
			m.httpDurationBuckets[i].Add(1)
		}
	}
	// The final bucket is the +Inf bucket and therefore counts every request.
	m.httpDurationBuckets[len(httpDurationBounds)].Add(1)
}

func NewMetrics() *Metrics { return &Metrics{} }

// ObserveUploadStaged records an archive streamed into its staged object.
func (m *Metrics) ObserveUploadStaged(bytes int64) {
	if m == nil || bytes < 0 {
		return
	}
	m.uploadsStaged.Add(1)
	m.uploadBytes.Add(uint64(bytes))
}

// ObserveUploadRejected records an upload refused at admission or while
// streaming, by bounded reason.
func (m *Metrics) ObserveUploadRejected(reason int) {
	if m == nil || reason < 0 || reason >= len(uploadRejectReasons) {
		return
	}
	m.uploadRejections[reason].Add(1)
}

// StreamOpened and StreamClosed track long-lived event streams. Streams are
// kept out of the HTTP duration histogram, where their 30-minute lifetimes
// would swamp request latency.
func (m *Metrics) StreamOpened() {
	if m == nil {
		return
	}
	m.streamsOpened.Add(1)
	m.activeStreams.Add(1)
}

func (m *Metrics) StreamClosed() {
	if m == nil {
		return
	}
	m.activeStreams.Add(-1)
}

// StreamRejected records a stream refused by the concurrency limits.
func (m *Metrics) StreamRejected() {
	if m == nil {
		return
	}
	m.streamsRejected.Add(1)
}

func (m *Metrics) RestoreStarted() {
	if m == nil {
		return
	}
	m.restoresStarted.Add(1)
	m.activeRestores.Add(1)
}

func (m *Metrics) RestoreFinished(err error) {
	if m == nil {
		return
	}
	m.activeRestores.Add(-1)
	if err != nil {
		m.restoresFailed.Add(1)
		return
	}
	m.restoresCompleted.Add(1)
}

func (m *Metrics) Write(w io.Writer) {
	if m == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "# HELP stepanel_up StePanel process health\n# TYPE stepanel_up gauge\nstepanel_up 1\n")
	_, _ = fmt.Fprintf(w, "# HELP stepanel_restore_jobs_started_total Restore jobs accepted\n# TYPE stepanel_restore_jobs_started_total counter\nstepanel_restore_jobs_started_total %d\n", m.restoresStarted.Load())
	_, _ = fmt.Fprintf(w, "# HELP stepanel_restore_jobs_completed_total Restore jobs completed successfully\n# TYPE stepanel_restore_jobs_completed_total counter\nstepanel_restore_jobs_completed_total %d\n", m.restoresCompleted.Load())
	_, _ = fmt.Fprintf(w, "# HELP stepanel_restore_jobs_failed_total Restore jobs that failed\n# TYPE stepanel_restore_jobs_failed_total counter\nstepanel_restore_jobs_failed_total %d\n", m.restoresFailed.Load())
	_, _ = fmt.Fprintf(w, "# HELP stepanel_restore_jobs_active Current restore jobs\n# TYPE stepanel_restore_jobs_active gauge\nstepanel_restore_jobs_active %d\n", m.activeRestores.Load())
	_, _ = fmt.Fprintf(w, "# HELP stepanel_http_requests_total HTTP requests served\n# TYPE stepanel_http_requests_total counter\nstepanel_http_requests_total %d\n", m.httpRequests.Load())
	_, _ = fmt.Fprintf(w, "# HELP stepanel_http_errors_total HTTP 5xx responses\n# TYPE stepanel_http_errors_total counter\nstepanel_http_errors_total %d\n", m.httpErrors.Load())
	_, _ = fmt.Fprintf(w, "# HELP stepanel_http_request_duration_seconds_total Cumulative HTTP request duration\n# TYPE stepanel_http_request_duration_seconds_total counter\nstepanel_http_request_duration_seconds_total %.6f\n", float64(m.httpDurationNanos.Load())/float64(time.Second))
	_, _ = fmt.Fprintf(w, "# HELP stepanel_http_responses_total HTTP responses by status class\n# TYPE stepanel_http_responses_total counter\n")
	for bucket := 1; bucket <= 5; bucket++ {
		_, _ = fmt.Fprintf(w, "stepanel_http_responses_total{class=\"%dxx\"} %d\n", bucket, m.httpStatus[bucket].Load())
	}
	_, _ = fmt.Fprintln(w, "# HELP stepanel_http_request_duration_seconds HTTP request duration histogram")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_http_request_duration_seconds histogram")
	for i, bound := range httpDurationBounds {
		_, _ = fmt.Fprintf(w, "stepanel_http_request_duration_seconds_bucket{le=\"%g\"} %d\n", bound, m.httpDurationBuckets[i].Load())
	}
	_, _ = fmt.Fprintf(w, "stepanel_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", m.httpDurationBuckets[len(httpDurationBounds)].Load())
	_, _ = fmt.Fprintf(w, "stepanel_http_request_duration_seconds_sum %.6f\n", float64(m.httpDurationNanos.Load())/float64(time.Second))
	_, _ = fmt.Fprintf(w, "stepanel_http_request_duration_seconds_count %d\n", m.httpRequests.Load())
	_, _ = fmt.Fprintln(w, "# HELP stepanel_state_errors_total Durable operation errors by bounded category")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_state_errors_total counter")
	for i, category := range stateMetricCategories {
		_, _ = fmt.Fprintf(w, "stepanel_state_errors_total{category=\"%s\"} %d\n", category, m.stateErrors[i].Load())
	}
	_, _ = fmt.Fprintln(w, "# HELP stepanel_sqlite_busy_errors_total SQLite busy or locked errors observed by durable state paths")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_sqlite_busy_errors_total counter")
	_, _ = fmt.Fprintf(w, "stepanel_sqlite_busy_errors_total %d\n", m.sqliteBusyErrors.Load())
	_, _ = fmt.Fprintf(w, "# HELP stepanel_uploads_staged_total Archive uploads streamed into a staged object\n# TYPE stepanel_uploads_staged_total counter\nstepanel_uploads_staged_total %d\n", m.uploadsStaged.Load())
	_, _ = fmt.Fprintf(w, "# HELP stepanel_upload_bytes_total Archive bytes streamed into staged objects\n# TYPE stepanel_upload_bytes_total counter\nstepanel_upload_bytes_total %d\n", m.uploadBytes.Load())
	_, _ = fmt.Fprintln(w, "# HELP stepanel_upload_rejections_total Archive uploads refused at admission or while streaming, by reason")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_upload_rejections_total counter")
	for i, reason := range uploadRejectReasons {
		_, _ = fmt.Fprintf(w, "stepanel_upload_rejections_total{reason=\"%s\"} %d\n", reason, m.uploadRejections[i].Load())
	}
	_, _ = fmt.Fprintf(w, "# HELP stepanel_event_streams_opened_total Server-sent event streams accepted\n# TYPE stepanel_event_streams_opened_total counter\nstepanel_event_streams_opened_total %d\n", m.streamsOpened.Load())
	_, _ = fmt.Fprintf(w, "# HELP stepanel_event_streams_rejected_total Server-sent event streams refused by concurrency limits\n# TYPE stepanel_event_streams_rejected_total counter\nstepanel_event_streams_rejected_total %d\n", m.streamsRejected.Load())
	_, _ = fmt.Fprintf(w, "# HELP stepanel_event_streams_active Open server-sent event streams\n# TYPE stepanel_event_streams_active gauge\nstepanel_event_streams_active %d\n", m.activeStreams.Load())
}

func writeReadinessMetrics(w io.Writer, checks map[string]ReadinessCheck) {
	_, _ = fmt.Fprintln(w, "# HELP stepanel_readiness_check_ready Whether a readiness check currently passes")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_readiness_check_ready gauge")
	for name, check := range checks {
		value := 0
		if check.Ready {
			value = 1
		}
		_, _ = fmt.Fprintf(w, "stepanel_readiness_check_ready{check=\"%s\"} %d\n", name, value)
	}
}

func writeJobMetrics(w io.Writer, jobs *Jobs) {
	stats := JobQueueStats{}
	inventoryErrors := 0
	if jobs != nil {
		var err error
		stats, err = jobs.QueueStats()
		if err != nil {
			inventoryErrors = 1
		}
	}
	_, _ = fmt.Fprintln(w, "# HELP stepanel_jobs_queued Current durable jobs waiting for a worker")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_jobs_queued gauge")
	_, _ = fmt.Fprintf(w, "stepanel_jobs_queued %d\n", stats.Queued)
	_, _ = fmt.Fprintln(w, "# HELP stepanel_jobs_running Current durable jobs held by workers")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_jobs_running gauge")
	_, _ = fmt.Fprintf(w, "stepanel_jobs_running %d\n", stats.Running)
	_, _ = fmt.Fprintln(w, "# HELP stepanel_jobs_dead_letter Jobs requiring operator review")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_jobs_dead_letter gauge")
	_, _ = fmt.Fprintf(w, "stepanel_jobs_dead_letter %d\n", stats.DeadLetter)
	_, _ = fmt.Fprintln(w, "# HELP stepanel_jobs_inventory_errors Whether durable job inventory collection failed")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_jobs_inventory_errors gauge")
	_, _ = fmt.Fprintf(w, "stepanel_jobs_inventory_errors %d\n", inventoryErrors)
}

func writeDatabaseMetrics(w io.Writer, diagnostics DatabaseDiagnostics) {
	up := 0
	if diagnostics.Available {
		up = 1
	}
	_, _ = fmt.Fprintf(w, "# HELP stepanel_database_diagnostics_up Database diagnostics collection succeeded\n# TYPE stepanel_database_diagnostics_up gauge\nstepanel_database_diagnostics_up{engine=%q} %d\n", diagnostics.Engine, up)
	for _, name := range []string{"connections", "active_connections", "long_transactions", "blocked_sessions", "deadlocks", "database_bytes"} {
		_, _ = fmt.Fprintf(w, "# TYPE stepanel_database_%s gauge\nstepanel_database_%s{engine=%q} %d\n", name, name, diagnostics.Engine, diagnostics.Values[name])
	}
}

func writeBackupScheduleMetrics(w io.Writer, schedules []BackupSchedule) {
	now := time.Now().UTC()
	var oldestAge float64
	var failures, withoutSuccess int
	for _, schedule := range schedules {
		failures += schedule.ConsecutiveFails
		if schedule.LastSuccess == nil {
			withoutSuccess++
			continue
		}
		age := now.Sub(*schedule.LastSuccess).Seconds()
		if age > oldestAge {
			oldestAge = age
		}
	}
	_, _ = fmt.Fprintln(w, "# HELP stepanel_backup_oldest_age_seconds Age of the oldest last-success time across scheduled backups")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_backup_oldest_age_seconds gauge")
	_, _ = fmt.Fprintf(w, "stepanel_backup_oldest_age_seconds %.0f\n", oldestAge)
	_, _ = fmt.Fprintln(w, "# HELP stepanel_backup_consecutive_failures Sum of consecutive scheduled backup failures")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_backup_consecutive_failures gauge")
	_, _ = fmt.Fprintf(w, "stepanel_backup_consecutive_failures %d\n", failures)
	_, _ = fmt.Fprintln(w, "# HELP stepanel_backup_schedules_without_success Scheduled backups that have never completed successfully")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_backup_schedules_without_success gauge")
	_, _ = fmt.Fprintf(w, "stepanel_backup_schedules_without_success %d\n", withoutSuccess)
}

func writeGitReleaseMetrics(w io.Writer, webRoot string) {
	var totalBytes int64
	var total int
	var inventoryErrors int
	root := filepath.Join(webRoot, "sites")
	entries, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			inventoryErrors++
		}
		entries = nil
	}
	for _, site := range entries {
		if !site.IsDir() {
			continue
		}
		children, err := os.ReadDir(filepath.Join(root, site.Name()))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				inventoryErrors++
			}
			continue
		}
		for _, release := range children {
			if !release.IsDir() || release.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(release.Name(), ".stepanel-previous-") {
				continue
			}
			size, err := gitReleaseSize(filepath.Join(root, site.Name(), release.Name()))
			if err == nil {
				total++
				totalBytes += size
			}
		}
	}
	_, _ = fmt.Fprintln(w, "# HELP stepanel_git_release_bytes Bytes retained by previous Git releases")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_git_release_bytes gauge")
	_, _ = fmt.Fprintf(w, "stepanel_git_release_bytes %d\n", totalBytes)
	_, _ = fmt.Fprintln(w, "# HELP stepanel_git_releases_total Number of previous Git releases retained")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_git_releases_total gauge")
	_, _ = fmt.Fprintf(w, "stepanel_git_releases_total %d\n", total)
	_, _ = fmt.Fprintln(w, "# HELP stepanel_git_release_inventory_errors Number of unreadable Git release inventory directories")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_git_release_inventory_errors gauge")
	_, _ = fmt.Fprintf(w, "stepanel_git_release_inventory_errors %d\n", inventoryErrors)
}
