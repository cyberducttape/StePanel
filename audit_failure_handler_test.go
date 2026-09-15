package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestMustAuditSucceeds(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	w := httptest.NewRecorder()
	if err := MustAudit(w, path, "admin", "hosting.account.mfa-reset", "customer", "detail"); err != nil {
		t.Fatalf("expected audit write to succeed: %v", err)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("expected no response body written on success, got %q", w.Body.String())
	}
	if err := VerifyAuditLog(path); err != nil {
		t.Fatalf("expected a verifiable audit event: %v", err)
	}
}

func TestMustAuditFailsClosed(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	w := httptest.NewRecorder()
	// appendAuditEvent requires a non-empty action; passing "" forces a
	// deterministic persistence failure without touching the filesystem.
	if err := MustAudit(w, path, "admin", "", "customer", "detail"); err == nil {
		t.Fatal("expected MustAudit to return an error when the audit write fails")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when the audit write fails, got %d", w.Code)
	}
}

func TestShouldAuditReturnsErrorWithoutWritingResponse(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := ShouldAudit(path, "admin", "", "customer", "detail"); err == nil {
		t.Fatal("expected ShouldAudit to surface the underlying audit error")
	}
	if err := ShouldAudit(path, "admin", "backup.schedule.updated", "customer", "detail"); err != nil {
		t.Fatalf("expected a valid audit write to succeed: %v", err)
	}
}
