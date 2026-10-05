package stepanel

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditChainRecordsActorAndVerifies(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := SecurityAuditRequired(path, "admin", "site.deployed", "account", "example.com"); err != nil {
		t.Fatal(err)
	}
	if err := SecurityAuditRequired(path, "admin", "site.backup.completed", "account", "checksum"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAuditLog(path); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	events := []AuditEvent{}
	for scanner.Scan() {
		var event AuditEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 2 || events[0].Actor != "admin" || events[0].Target != "account" || events[1].PreviousHash != events[0].Hash || events[1].Sequence != 2 {
		t.Fatalf("audit events = %#v", events)
	}
}

func TestAuditEventsFiltersVerifiedHistory(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("q", 32))
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := SecurityAuditRequired(path, "admin", "site.deployed", "account", "example.com"); err != nil {
		t.Fatal(err)
	}
	if err := SecurityAuditRequired(path, "admin", "site.backup.completed", "account", "checksum"); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{AuditLog: path}}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/audit/events?action=site.deployed", nil)
	app.auditEvents(response, request)
	if response.Code != http.StatusOK || strings.Count(response.Body.String(), `"action":"site.deployed"`) != 1 || strings.Contains(response.Body.String(), "site.backup.completed") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAuditRequiresStrongSigningKey(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "")
	previousKeyPath := auditKeyPath
	auditKeyPath = filepath.Join(t.TempDir(), "missing-audit.key")
	t.Cleanup(func() { auditKeyPath = previousKeyPath })
	if err := SecurityAuditRequired(filepath.Join(t.TempDir(), "audit.jsonl"), "system", "test.action", "site", "detail"); err == nil {
		t.Fatal("audit event was accepted without a signing key")
	}
	auditMu.Lock()
	auditPersistenceErr = nil
	auditMu.Unlock()
}

func TestVerifyAuditLogRejectsTampering(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("s", 32))
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := SecurityAuditRequired(path, "admin", "test.action", "site", "original"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"detail":"original"`, `"detail":"tampered"`, 1))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAuditLog(path); err == nil {
		t.Fatal("tampered audit log passed verification")
	}
}

func TestVerifyAuditLogRejectsWrongKey(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("a", 32))
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := SecurityAuditRequired(path, "admin", "test.action", "site", "detail"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("b", 32))
	if err := VerifyAuditLog(path); err == nil {
		t.Fatal("audit log passed verification with the wrong key")
	}
	if err := SecurityAuditRequired(path, "admin", "second.action", "site", "detail"); err == nil {
		t.Fatal("audit chain accepted an event signed with a replacement key")
	}
	auditMu.Lock()
	auditPersistenceErr = nil
	auditMu.Unlock()
}

func TestVerifyAuditLogRejectsRewrittenState(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("w", 32))
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := SecurityAuditRequired(path, "admin", "test.action", "site", "detail"); err != nil {
		t.Fatal(err)
	}
	stateData, err := os.ReadFile(path + ".state")
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(stateData, &state); err != nil {
		t.Fatal(err)
	}
	state["sequence"] = float64(0)
	state["hash"] = ""
	stateData, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".state", stateData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAuditLog(path); err == nil {
		t.Fatal("audit log passed verification with rewritten state")
	}
}

func TestAuditPreservesUnsignedLegacyLog(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("l", 32))
	root := t.TempDir()
	path := filepath.Join(root, "audit.jsonl")
	if err := os.WriteFile(path, []byte("{\"legacy\":true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SecurityAuditRequired(path, "system", "service.started", "stepanel", "upgrade"); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(path + ".legacy-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("legacy logs = %#v, error = %v", matches, err)
	}
	if err := VerifyAuditLog(path); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAuditEventRejectsBadTimestampAndIdentity(t *testing.T) {
	if err := validateAuditEvent(AuditEvent{Sequence: 1, Time: "not-a-timestamp", Actor: "admin", Action: "test"}); err == nil {
		t.Fatal("invalid timestamp was accepted")
	}
	if err := validateAuditEvent(AuditEvent{Sequence: 1, Time: "2026-09-04T00:00:00Z", Actor: "", Action: "test"}); err == nil {
		t.Fatal("empty actor was accepted")
	}
}

func TestAuditChainContinuesAcrossRotation(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("r", 32))
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := SecurityAuditRequired(path, "admin", "one", "site", "first"); err != nil {
		t.Fatal(err)
	}
	if err := SecurityAuditRequired(path, "admin", "two", "site", "second"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := SecurityAuditRequired(path, "admin", "three", "site", "third"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAuditLog(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event AuditEvent
	if err := json.Unmarshal(data, &event); err != nil || event.Sequence != 3 || event.PreviousHash == "" {
		t.Fatalf("rotated audit event = %#v, error = %v", event, err)
	}
}
