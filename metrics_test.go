package stepanel

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/audit"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

func TestMetricsWrite(t *testing.T) {
	m := NewMetrics()
	m.RestoreStarted()
	m.RestoreFinished(nil)
	m.RestoreStarted()
	m.RestoreFinished(assertionError{})

	var output strings.Builder
	m.Write(&output)
	for _, want := range []string{
		"stepanel_up 1",
		"stepanel_restore_jobs_started_total 2",
		"stepanel_restore_jobs_completed_total 1",
		"stepanel_restore_jobs_failed_total 1",
		"stepanel_restore_jobs_active 0",
		"stepanel_http_request_duration_seconds_bucket{le=\"+Inf\"}",
		"stepanel_http_request_duration_seconds_count 0",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("metrics output missing %q:\n%s", want, output.String())
		}
	}
}

func TestMetricsDurationHistogramIsCumulativeAndNonNegative(t *testing.T) {
	m := NewMetrics()
	m.ObserveHTTP(200, 20*time.Millisecond)
	m.ObserveHTTP(500, -time.Second)
	var output strings.Builder
	m.Write(&output)
	if !strings.Contains(output.String(), "stepanel_http_request_duration_seconds_count 2") {
		t.Fatalf("histogram count missing: %s", output.String())
	}
	if !strings.Contains(output.String(), "stepanel_http_request_duration_seconds_sum 0.020000") {
		t.Fatalf("negative duration was not clamped: %s", output.String())
	}
}

func TestMetricsExposeStateCategoriesAndSQLiteBusy(t *testing.T) {
	m := NewMetrics()
	m.ObserveStateError("persistence")
	m.ObserveSQLiteError(assertionErrorWithMessage{message: "database is locked (SQLITE_BUSY)"})
	var output strings.Builder
	m.Write(&output)
	for _, want := range []string{
		`stepanel_state_errors_total{category="persistence"} 1`,
		"stepanel_sqlite_busy_errors_total 1",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("metrics output missing %q:\n%s", want, output.String())
		}
	}
}

func TestHostFilesystemMetricsReportEvidenceWithoutExposingPaths(t *testing.T) {
	root := t.TempDir()
	var output strings.Builder
	writeHostFilesystemMetrics(&output, Config{
		WebRoot:        root,
		BackupRoot:     root,
		DBDataDir:      filepath.Join(root, "missing-database-path"),
		ControlPlaneDB: filepath.Join(root, "control", "panel.db"),
	})
	text := output.String()
	for _, want := range []string{
		`stepanel_host_filesystem_probe_up{role="sites"} 1`,
		`stepanel_host_filesystem_available_bytes{role="sites"}`,
		`stepanel_host_filesystem_total_bytes{role="sites"}`,
		`stepanel_host_filesystem_available_inodes{role="sites"}`,
		`stepanel_host_filesystem_total_inodes{role="sites"}`,
		`stepanel_host_filesystem_probe_up{role="databases"} 0`,
		`stepanel_host_filesystem_probe_up{role="control_plane"} 0`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("filesystem metrics missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, root) {
		t.Fatalf("filesystem metrics exposed a host path:\n%s", text)
	}
}

func TestSiteRuntimeMetricsExposeFPMAndEnforcedResourceEvidence(t *testing.T) {
	var output strings.Builder
	writeSiteRuntimeSamples(&output, map[string]siteRuntimeMetricSample{
		"shop": {
			site: "shop", collectedAt: time.Unix(1_800_000_000, 0).UTC(),
			fpmConfigured: true, fpmAvailable: true,
			pools:             []rootbroker.FPMStatusPool{{PHPVersion: "8.3", State: "available", ActiveProcesses: 8, MaxChildren: 8, ListenQueue: 3, SlowRequests: 11, Saturated: true}},
			resourceAvailable: true, memoryCurrent: 900, memoryMax: 1000, tasksCurrent: 18, tasksMax: 20,
		},
	})
	for _, want := range []string{
		`stepanel_site_fpm_configured{site="shop"} 1`,
		`stepanel_site_fpm_probe_up{site="shop"} 1`,
		`stepanel_site_fpm_saturated{site="shop",php_version="8.3"} 1`,
		`stepanel_site_fpm_active_workers{site="shop",php_version="8.3"} 8`,
		`stepanel_site_fpm_max_children{site="shop",php_version="8.3"} 8`,
		`stepanel_site_fpm_listen_queue{site="shop",php_version="8.3"} 3`,
		`stepanel_site_fpm_slow_requests{site="shop",php_version="8.3"} 11`,
		`stepanel_site_resource_memory_current_bytes{site="shop"} 900`,
		`stepanel_site_resource_memory_max_bytes{site="shop"} 1000`,
		`stepanel_site_resource_tasks_current{site="shop"} 18`,
		`stepanel_site_resource_tasks_max{site="shop"} 20`,
		`stepanel_site_resource_sample_timestamp_seconds{site="shop"} 1800000000`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("site runtime metrics missing %q:\n%s", want, output.String())
		}
	}
}

func TestSiteRuntimeMetricsMarkUnavailableProbesWithoutInventingUsage(t *testing.T) {
	var output strings.Builder
	writeSiteRuntimeSamples(&output, map[string]siteRuntimeMetricSample{
		"shop": {site: "shop", collectedAt: time.Unix(1_800_000_000, 0).UTC(), fpmConfigured: true},
	})
	for _, want := range []string{
		`stepanel_site_fpm_configured{site="shop"} 1`,
		`stepanel_site_fpm_probe_up{site="shop"} 0`,
		`stepanel_site_resource_probe_up{site="shop"} 0`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("unavailable probe metric %q missing:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "memory_current_bytes{site=\"shop\"}") || strings.Contains(output.String(), "tasks_current{site=\"shop\"}") {
		t.Fatalf("unavailable probes emitted fabricated usage values:\n%s", output.String())
	}
}

func TestGitReleaseMetricsExposeInventoryReadFailures(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sites"), []byte("not-a-directory"), 0600); err != nil {
		t.Fatal(err)
	}

	var output strings.Builder
	writeGitReleaseMetrics(&output, Config{WebRoot: root})
	if !strings.Contains(output.String(), "stepanel_git_release_inventory_errors 1") {
		t.Fatalf("inventory error metric missing: %s", output.String())
	}
}

func TestGitReleaseMetricsCacheRefreshesAtMostOncePerInterval(t *testing.T) {
	var cache gitReleaseMetricsCache
	current := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return current }
	refreshes := 0
	refresh := func() gitReleaseMetricsSnapshot {
		refreshes++
		return gitReleaseMetricsSnapshot{totalBytes: int64(refreshes * 100), total: refreshes}
	}

	first, firstAt := cache.get("web-root", clock, refresh)
	current = current.Add(gitReleaseMetricsRefreshInterval - time.Nanosecond)
	cached, cachedAt := cache.get("web-root", clock, refresh)
	if first != cached || !firstAt.Equal(cachedAt) || refreshes != 1 {
		t.Fatalf("cache before expiry = %#v at %s; want %#v at %s from one refresh", cached, cachedAt, first, firstAt)
	}

	current = current.Add(2 * time.Nanosecond)
	updated, updatedAt := cache.get("web-root", clock, refresh)
	if updated.totalBytes != 200 || updated.total != 2 || !updatedAt.Equal(current) || refreshes != 2 {
		t.Fatalf("cache after expiry = %#v at %s with %d refreshes", updated, updatedAt, refreshes)
	}
}

func TestGitReleaseMetricsCacheCoalescesConcurrentInitialRefresh(t *testing.T) {
	var cache gitReleaseMetricsCache
	started := make(chan struct{})
	release := make(chan struct{})
	var refreshes int
	var refreshMu sync.Mutex
	refresh := func() gitReleaseMetricsSnapshot {
		refreshMu.Lock()
		refreshes++
		refreshMu.Unlock()
		close(started)
		<-release
		return gitReleaseMetricsSnapshot{totalBytes: 42, total: 1}
	}
	now := time.Now
	results := make(chan gitReleaseMetricsSnapshot, 2)
	for range 2 {
		go func() {
			snapshot, _ := cache.get("web-root", now, refresh)
			results <- snapshot
		}()
	}
	<-started
	close(release)
	for range 2 {
		if got := <-results; got.totalBytes != 42 || got.total != 1 {
			t.Fatalf("concurrent cached snapshot = %#v", got)
		}
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	if refreshes != 1 {
		t.Fatalf("initial refreshes = %d, want 1", refreshes)
	}
}

func TestJobMetricsExposeInventoryReadFailures(t *testing.T) {
	var output strings.Builder
	writeJobMetrics(&output, NewJobs())
	if !strings.Contains(output.String(), "stepanel_jobs_oldest_queued_age_seconds 0") {
		t.Fatalf("oldest queued age metric missing: %s", output.String())
	}
	if !strings.Contains(output.String(), "stepanel_jobs_inventory_errors 1") {
		t.Fatalf("job inventory error metric missing: %s", output.String())
	}
}

func TestOperatorNotificationMetricsExposeUnavailableOutbox(t *testing.T) {
	var output strings.Builder
	writeOperatorNotificationMetrics(&output, nil, Config{})
	if !strings.Contains(output.String(), "stepanel_operator_notification_outbox_up 0") || !strings.Contains(output.String(), "stepanel_operator_notifications_oldest_pending_age_seconds 0") {
		t.Fatalf("notification metrics missing unavailable/age signals: %s", output.String())
	}
}

func TestAuditOutboxMetricsExposePendingAge(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := newAuditOutboxStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.persist(t.Context(), "operator", "site.update", "example", "test"); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	writeAuditOutboxMetrics(&output, store)
	for _, want := range []string{"stepanel_audit_outbox_up 1", "stepanel_audit_outbox_pending 1", "stepanel_audit_outbox_oldest_pending_age_seconds 0"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("audit outbox metrics missing %q:\n%s", want, output.String())
		}
	}
}

func TestCapacityReservationMetricsAggregatePerFilesystem(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "capacity-metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO capacity_reservations(reservation_id,device,bytes,workflow,staging_path,created_at) VALUES
		('a',42,200,'import','/private/site-a',?),
		('b',42,100,'backup','/private/site-b',?),
		('c',84,40,'restore','/private/site-c',?)`, now-90, now-30, now); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	writeCapacityReservationMetrics(&output, db)
	for _, want := range []string{
		"stepanel_capacity_reservations_up 1",
		"stepanel_capacity_reservations_active 3",
		"stepanel_capacity_reserved_bytes 340",
		`stepanel_capacity_reserved_bytes_by_device{device="42"} 300`,
		`stepanel_capacity_reserved_bytes_by_device{device="84"} 40`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("capacity reservation metric missing %q:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "/private/") || strings.Contains(output.String(), "workflow=") {
		t.Fatalf("capacity metrics expose private reservation metadata: %s", output.String())
	}
}

func TestCapacityReservationMetricsReportUnavailableLedger(t *testing.T) {
	var output strings.Builder
	writeCapacityReservationMetrics(&output, nil)
	for _, want := range []string{
		"stepanel_capacity_reservations_up 0",
		"stepanel_capacity_reservations_active 0",
		"stepanel_capacity_reserved_bytes 0",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("unavailable capacity metric missing %q:\n%s", want, output.String())
		}
	}
}

type assertionError struct{}

func (assertionError) Error() string { return "test failure" }

type assertionErrorWithMessage struct{ message string }

func (e assertionErrorWithMessage) Error() string { return e.message }

func TestAuditWriterMetricsExposeLatencyAndFailures(t *testing.T) {
	var output strings.Builder
	writeAuditWriterMetrics(&output, audit.Health{Writes: 4, FailureCount: 1, LockWait: 1500 * time.Millisecond, WriteTime: 250 * time.Millisecond})
	for _, want := range []string{
		"# TYPE stepanel_audit_write_duration_seconds summary",
		"stepanel_audit_write_duration_seconds_sum 0.25\n",
		"stepanel_audit_write_duration_seconds_count 4\n",
		"stepanel_audit_lock_wait_seconds_sum 1.5\n",
		"stepanel_audit_lock_wait_seconds_count 4\n",
		"stepanel_audit_write_failures_total 1\n",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("audit writer metrics missing %q:\n%s", want, output.String())
		}
	}
}

func TestBackupScheduleMetricsExposePerSiteAgeForEnabledSchedules(t *testing.T) {
	succeeded := time.Now().UTC().Add(-26 * time.Hour)
	attempted := time.Now().UTC().Add(-30 * time.Minute)
	var output strings.Builder
	writeBackupScheduleMetrics(&output, []BackupSchedule{
		{Site: "example", Enabled: true, IntervalMinutes: 1440, LastSuccess: &succeeded},
		{Site: "paused", Enabled: false, IntervalMinutes: 60, LastSuccess: &succeeded},
		{Site: "fresh", Enabled: true, IntervalMinutes: 60, LastRun: &attempted, ConsecutiveFails: 2},
	})
	text := output.String()
	for _, want := range []string{
		`stepanel_backup_interval_seconds{site="example"} 86400`,
		`stepanel_backup_last_success_age_seconds{site="example"} 936`,
		`stepanel_backup_success_present{site="example"} 1`,
		`stepanel_backup_success_present{site="fresh"} 0`,
		`stepanel_backup_consecutive_failures_by_site{site="fresh"} 2`,
		`stepanel_backup_last_attempt_age_seconds{site="fresh"} 1800`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("backup metrics missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{
		`stepanel_backup_success_present{site="paused"}`,
		`stepanel_backup_last_success_age_seconds{site="fresh"}`,
		`stepanel_backup_consecutive_failures_by_site{site="paused"}`,
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("backup metrics include %s, which is disabled or has no successful result:\n%s", unwanted, text)
		}
	}
}

func TestCertificateRenewalMetricsExposeLatestDurableFailureEvidence(t *testing.T) {
	started := time.Unix(1793385600, 0).UTC()
	var output strings.Builder
	writeCertificateRenewalMetrics(&output, []Job{
		{ID: "cert-failed", Kind: "certificate.issue", User: "example.test", State: "failed", StartedAt: started},
		{ID: "ignored", Kind: "certificate.issue", User: "invalid domain", State: "failed", StartedAt: started},
		{ID: "ignored-state", Kind: "certificate.issue", User: "other.example", State: "unknown", StartedAt: started},
	}, true)
	text := output.String()
	for _, want := range []string{
		"stepanel_certificate_renewal_inventory_up 1",
		`stepanel_certificate_renewal_latest_job_timestamp_seconds{domain="example.test",state="failed"} 1793385600`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("certificate metrics missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "cert-failed") || strings.Contains(text, "invalid domain") || strings.Contains(text, "unknown") {
		t.Fatalf("certificate metrics contain unsafe or unbounded job labels:\n%s", text)
	}
	var unavailable strings.Builder
	writeCertificateRenewalMetrics(&unavailable, nil, false)
	if !strings.Contains(unavailable.String(), "stepanel_certificate_renewal_inventory_up 0") {
		t.Fatalf("unavailable durable evidence was not reported:\n%s", unavailable.String())
	}
}
