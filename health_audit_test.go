package stepanel

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/audit"
)

func TestAuditReadinessReportsCurrentStateAndRecoveredFailures(t *testing.T) {
	if check := auditReadiness(audit.Health{}); !check.Ready || check.Detail != "" {
		t.Fatalf("healthy audit readiness = %+v", check)
	}
	failing := auditReadiness(audit.Health{LastError: errors.New("disk full"), FailureCount: 3})
	if failing.Ready || !strings.Contains(failing.Detail, "disk full") || !strings.Contains(failing.Detail, "3 audit write failures") {
		t.Fatalf("failing audit readiness = %+v", failing)
	}
	last := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	recovered := auditReadiness(audit.Health{FailureCount: 3, LastFailure: last, LastSuccess: last.Add(time.Minute)})
	if !recovered.Ready || !strings.Contains(recovered.Detail, "recovered: 3 audit write failures") || !strings.Contains(recovered.Detail, "2026-10-17T08:00:00Z") {
		t.Fatalf("recovered audit readiness = %+v", recovered)
	}
}
