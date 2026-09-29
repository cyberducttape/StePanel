package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestLogger(t *testing.T) (*defaultLogger, string) {
	t.Helper()
	root := t.TempDir()
	keyPath := filepath.Join(root, "audit.key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	previousKeyPath := auditKeyPath
	TestSetKeyPath(keyPath)
	t.Setenv("STEPANEL_AUDIT_KEY", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "")
	t.Cleanup(func() { TestSetKeyPath(previousKeyPath) })
	TestResetPersistenceError()
	return New(filepath.Join(root, "audit.jsonl")).(*defaultLogger), root
}

func TestLoggerWritesVerifiesAndFiltersSignedEvents(t *testing.T) {
	logger, _ := newTestLogger(t)
	ctx := context.Background()
	if err := logger.LogAs(ctx, "admin", "site.create", "site-a", "created"); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogAs(ctx, "admin", "site.delete", "site-b", "deleted"); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogAs(ctx, "operator", "site.create", "site-c", "created"); err != nil {
		t.Fatal(err)
	}
	if err := logger.Verify(logger.path); err != nil {
		t.Fatalf("verify signed log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/audit?target=site-c", nil)
	resp := httptest.NewRecorder()
	logger.Events(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("filtered events status = %d body=%s", resp.Code, resp.Body)
	}
	var payload struct {
		Events    []Event `json:"events"`
		Integrity string  `json:"integrity"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Events) != 1 || payload.Events[0].Target != "site-c" || payload.Integrity != "verified" {
		t.Fatalf("filtered payload = %#v", payload)
	}

	req = httptest.NewRequest(http.MethodGet, "/audit?limit=0", nil)
	resp = httptest.NewRecorder()
	logger.Events(resp, req)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid limit status = %d", resp.Code)
	}
}

func TestLoggerRejectsTamperedLogAndMissingIdentity(t *testing.T) {
	logger, _ := newTestLogger(t)
	if err := logger.LogAs(context.Background(), "admin", "site.create", "site-a", "created"); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogAs(context.Background(), "", "site.update", "site-a", "bad"); err == nil {
		t.Fatal("missing actor was accepted")
	}
	data, err := os.ReadFile(logger.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logger.path, append(data, []byte(`{"broken":true}`+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := logger.Verify(logger.path); err == nil {
		t.Fatal("tampered audit log verified")
	}
	req := httptest.NewRequest(http.MethodGet, "/audit", nil)
	resp := httptest.NewRecorder()
	logger.Events(resp, req)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("tampered events status = %d", resp.Code)
	}
}

func TestAuditClassificationAndHandlerLevels(t *testing.T) {
	if got := MUST_AUDIT.String(); got != "MUST_AUDIT" {
		t.Fatalf("MUST_AUDIT string = %q", got)
	}
	if got := AuditLevel(99).String(); got != "UNKNOWN" {
		t.Fatalf("unknown level string = %q", got)
	}
	if GetClassification("site.delete") == nil || GetClassification("unknown") != nil {
		t.Fatal("audit classifications are incorrect")
	}
	var messages []string
	handler := NewDefaultAuditHandler(func(_ AuditLevel, message string) { messages = append(messages, message) })
	if err := handler.Audit(SHOULD_AUDIT, "site.create", "admin", "site-a", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := handler.AuditOrFail(MUST_AUDIT, "site.delete", "admin", "site-a", "ok"); err == nil {
		t.Fatal("MUST_AUDIT unexpectedly succeeded")
	}
	if err := handler.AuditWithResult(MUST_AUDIT, "site.delete", "admin", "site-a", "failed", os.ErrPermission); err == nil {
		t.Fatal("failed MUST_AUDIT unexpectedly succeeded")
	}
	if len(messages) != 3 {
		t.Fatalf("handler messages = %d", len(messages))
	}
}
