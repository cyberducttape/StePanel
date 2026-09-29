package audit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModuleHelpersWithoutDefaultLogger(t *testing.T) {
	previous := defaultAudit
	SetDefault(nil)
	t.Cleanup(func() { SetDefault(previous) })
	if Log("read", "site-a", "details") != nil || LogAs("admin", "read", "site-a", "details") != nil || PersistenceError() != nil || Verify("missing") != nil {
		t.Fatal("nil default logger did not no-op")
	}
}

func TestUnconfiguredEventsAndSecurityChecks(t *testing.T) {
	logger := New("")
	req := httptest.NewRequest(http.MethodGet, "/audit", nil)
	resp := httptest.NewRecorder()
	logger.Events(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("unconfigured events status = %d", resp.Code)
	}

	resp = httptest.NewRecorder()
	logger.SecurityChecks(resp, req)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("security checks status = %d", resp.Code)
	}
}

func TestMustAndShouldLogUseDefaultLogger(t *testing.T) {
	logger, _ := newTestLogger(t)
	previous := defaultAudit
	SetDefault(logger)
	t.Cleanup(func() { SetDefault(previous) })
	if err := ShouldLog("admin", "site.create", "site-a", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := MustLog(httptest.NewRecorder(), "admin", "site.create", "site-a", "ok"); err != nil {
		t.Fatal(err)
	}
}
