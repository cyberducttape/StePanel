package audit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type failingLogger struct{ err error }

func (l failingLogger) Log(context.Context, string, string, string) error           { return l.err }
func (l failingLogger) LogAs(context.Context, string, string, string, string) error { return l.err }
func (l failingLogger) Events(http.ResponseWriter, *http.Request)                   {}
func (l failingLogger) SecurityChecks(http.ResponseWriter, *http.Request)           {}
func (l failingLogger) PersistenceError() error                                     { return l.err }
func (l failingLogger) Verify(string) error                                         { return l.err }

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

func TestModuleHelpersSurfaceLoggerFailures(t *testing.T) {
	expected := errors.New("persistence failed")
	previous := defaultAudit
	SetDefault(failingLogger{err: expected})
	t.Cleanup(func() { SetDefault(previous) })
	if !errors.Is(Log("read", "site-a", "details"), expected) ||
		!errors.Is(LogAs("admin", "read", "site-a", "details"), expected) ||
		!errors.Is(PersistenceError(), expected) || !errors.Is(Verify("audit"), expected) {
		t.Fatal("module helper did not surface logger failure")
	}
	response := httptest.NewRecorder()
	if !errors.Is(MustLog(response, "admin", "delete", "site-a", "details"), expected) || response.Code != http.StatusServiceUnavailable {
		t.Fatalf("MustLog response = %d", response.Code)
	}
	if !errors.Is(ShouldLog("admin", "delete", "site-a", "details"), expected) {
		t.Fatal("ShouldLog did not surface logger failure")
	}
}
