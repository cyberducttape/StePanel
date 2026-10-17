package stepanel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/audit"
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

func TestGitReleaseMetricsExposeInventoryReadFailures(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sites"), []byte("not-a-directory"), 0600); err != nil {
		t.Fatal(err)
	}

	var output strings.Builder
	writeGitReleaseMetrics(&output, root)
	if !strings.Contains(output.String(), "stepanel_git_release_inventory_errors 1") {
		t.Fatalf("inventory error metric missing: %s", output.String())
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
